package thread

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
)

func testShellCall(id uint64) history.Entry {
	return testMain(id, "tool_use", `,"tool_use_id":"c","name":"Bash","input":{"command":"original"}`)
}
func testShell(t *testing.T, f *Fold, id uint64, status string, active bool, ended uint64) Item {
	t.Helper()
	for _, item := range f.Items() {
		if item.Kind == "agent" {
			t.Fatal("shell created agent", item)
		}
		if item.ID == id {
			if item.Kind != "tool_call" || item.Status != status || item.Active != active || item.EndedOrder != ended || item.Order != id || item.Rev < id {
				t.Fatalf("shell: %#v %s", item, item.Content)
			}
			testContent(t, item, "input", map[string]any{"command": "original"})
			return item
		}
	}
	t.Fatal("missing shell", f.Items())
	return Item{}
}
func TestShellLifecycleOrders(t *testing.T) {
	t.Parallel()
	var permute func([]int, []int)
	permute = func(prefix, rest []int) {
		if len(rest) > 0 {
			for i, n := range rest {
				permute(append(append([]int(nil), prefix...), n), append(append([]int(nil), rest[:i]...), rest[i+1:]...))
			}
			return
		}
		t.Run(fmt.Sprint(prefix), func(t *testing.T) {
			facts := []history.Entry{testShellCall(0), testMain(0, "tool_result", `,"tool_use_id":"c","is_error":false,"result_summary":"launch"`), testTask(0, "background_task_started", `,"tool_call_id":"c"`), testTask(0, "background_task_updated", `,"status":"completed","summary":"winner"`)}
			var entries []history.Entry
			var creation, ended uint64
			for _, n := range prefix {
				e := facts[n]
				e.ID = uint64(len(entries) + 1)
				entries = append(entries, e)
				if n == 0 {
					creation = e.ID
				}
				if n == 3 {
					ended = e.ID
				}
			}
			f := testMainReplay(t, entries)
			item := testShell(t, f, creation, "finished", false, max(creation, ended))
			testContent(t, item, "task_id", "task")
			var content map[string]json.RawMessage
			_ = json.Unmarshal(item.Content, &content)
			if content["result"] == nil || content["task_link"] == nil || content["task_report"] == nil || len(f.Items()) != 1 {
				t.Fatal("missing joined evidence", string(item.Content))
			}
			incremental := New("a")
			for _, e := range entries {
				testFeed(t, incremental, e)
			}
			if !reflect.DeepEqual(incremental.Items(), f.Items()) {
				t.Fatal("incremental differs")
			}
		})
	}
	permute(nil, []int{0, 1, 2, 3})
}
func TestShellLateLinkAndEnrichment(t *testing.T) {
	t.Parallel()
	for _, resultFirst := range []bool{false, true} {
		entries := []history.Entry{testShellCall(1), testMain(2, "turn_end", `,"stop_reason":"end_turn"`), testMain(3, "tool_result", `,"tool_use_id":"c","is_error":false,"result_summary":"launch"`), testTask(4, "background_task_started", `,"tool_call_id":"c"`)}
		if resultFirst {
			entries[1], entries[2] = entries[2], entries[1]
			entries[1].ID, entries[2].ID = 2, 3
		}
		f := testMainReplay(t, entries)
		item := testShell(t, f, 1, "running", true, 0)
		testContent(t, item, "ending", nil)
		testFeed(t, f, testTask(5, "background_task_updated", `,"status":"stopping"`), testTask(6, "background_task_updated", `,"summary":"enrich","patch":"{\"status\":\"failed\"}"`), testTask(7, "background_task_progress", `,"description":"progress"`), testEntry(8, "background_task_roster", `{"tasks":[]}`), testMain(9, "turn_end", `,"stop_reason":"end_turn"`))
		item = testShell(t, f, 1, "stopping", true, 0)
		if item.Rev != 6 {
			t.Fatal("non-evidence revised shell", item)
		}
		testFeed(t, f, testTask(10, "background_task_updated", `,"status":"completed"`), testTask(11, "background_task_updated", `,"summary":"after final"`))
		item = testShell(t, f, 1, "finished", false, 10)
		var content map[string]json.RawMessage
		_ = json.Unmarshal(item.Content, &content)
		if content["task_update"] == nil || content["result"] == nil {
			t.Fatal(string(item.Content))
		}
	}
}
func TestShellLifetimeScopes(t *testing.T) {
	t.Parallel()
	for _, source := range []*history.SessionProvenance{nil, testSource("claude", "s")} {
		entries := []history.Entry{testMain(1, "tool_result", `,"tool_use_id":"c","is_error":false,"result_summary":"early"`), testShellCall(2), testDurable(3, "background_task_observed", `,"task_id":"task"`), testDurable(4, "background_task_linked", `,"task_id":"task","tool_call_id":"c"`), testDurable(5, "background_task_outcome", `,"task_id":"task","status":"completed"`)}
		for i := range entries {
			entries[i].Session = source
		}
		f := testMainReplay(t, entries)
		testShell(t, f, 2, "finished", false, 5)
		next := testDurable(6, "background_task_observed", `,"task_id":"task"`)
		next.Payload = []byte(strings.ReplaceAll(string(next.Payload), "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"))
		next.Session = source
		use := testShellCall(7)
		use.Session = source
		link := testDurable(8, "background_task_linked", `,"task_id":"task","tool_call_id":"c"`)
		link.Payload = []byte(strings.ReplaceAll(string(link.Payload), "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"))
		link.Session = source
		testFeed(t, f, next, use, link)
		testShell(t, f, 2, "finished", false, 5)
		testShell(t, f, 7, "running", true, 0)
	}
	for _, boundary := range []string{"operator_reset", "idle_sleep", "daemon_restart"} {
		entries := []history.Entry{testShellCall(1), testDivider(2, boundary, "s", "n", "claude", "claude"), testDurable(3, "background_task_observed", `,"task_id":"task"`), testDurable(4, "background_task_linked", `,"task_id":"task","tool_call_id":"c"`)}
		f := testMainReplay(t, entries)
		item := testShell(t, f, 1, "running", true, 0)
		var data map[string]any
		_ = json.Unmarshal(item.Content, &data)
		if (data["task_id"] != nil) != (boundary == "daemon_restart") {
			t.Fatal("cross-scope promotion", string(item.Content))
		}
	}
	entries := []history.Entry{testShellCall(1), testDurable(2, "background_task_observed", `,"task_id":"task"`), testDurable(3, "background_task_linked", `,"task_id":"task","tool_call_id":"c"`)}
	entries[0].Session = testSource("claude", "old")
	entries[1].Session = testSource("claude", "new")
	entries[2].Session = entries[1].Session
	testContent(t, testShell(t, testMainReplay(t, entries), 1, "running", true, 0), "task_id", nil)
}
func TestShellFinalsAndRecovery(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"completed", "failed", "stopped", "gone", "ended_with_session", "unknown"} {
		entries := []history.Entry{testShellCall(1), testTask(2, "background_task_started", `,"tool_call_id":"c"`), testTask(3, "background_task_updated", `,"status":"`+status+`","summary":"winner"`), testTask(4, "background_task_updated", `,"status":"failed","summary":"conflict"`)}
		want := status
		if want == "completed" {
			want = "finished"
		}
		f := testMainReplay(t, entries)
		item := testShell(t, f, 1, want, false, 3)
		rev := item.Rev
		testFeed(t, f, testTask(5, "background_task_updated", `,"status":"`+status+`"`))
		if testShell(t, f, 1, want, false, 3).Rev != rev {
			t.Fatal("redundant final revised")
		}
	}
	for _, ref := range []string{"2", "null", "0", "1", "5", "99999999999999999"} {
		entries := []history.Entry{testShellCall(1), testTask(2, "background_task_started", `,"tool_call_id":"c"`), testDivider(3, "operator_reset", "s", "n", "claude", "claude"), testEntry(4, "agent_ended_with_session", `{"conversation_id":"a","task_id":"task","task_observed_entry_id":`+ref+`,"cause":"idle_sleep","occurred_at":"2026-10-09T00:00:00Z"}`)}
		f := testMainReplay(t, entries)
		if ref == "2" {
			item := testShell(t, f, 1, "ended_with_session", false, 4)
			var content map[string]map[string]any
			_ = json.Unmarshal(item.Content, &content)
			if content["ending"]["cause"] != "idle_sleep" {
				t.Fatal("lost cause")
			}
		} else {
			testShell(t, f, 1, "running", true, 0)
		}
	}
	entries := []history.Entry{testShellCall(1), testDurable(2, "background_task_observed", `,"task_id":"task"`), testDurable(3, "background_task_linked", `,"task_id":"task","tool_call_id":"c"`), testDurable(4, "background_task_outcome", `,"task_id":"task","status":"failed"`), testDurable(5, "agent_ended_with_session", `,"task_id":"task","tool_call_id":"c","cause":"daemon_restart"`), testTask(6, "background_task_updated", `,"status":"failed","summary":"mapped winner"`)}
	item := testShell(t, testMainReplay(t, entries), 1, "failed", false, 4)
	testContent(t, item, "task_report", map[string]any{"task_id": "task", "status": "failed", "summary": "mapped winner"})
	f := testMainReplay(t, []history.Entry{testShellCall(1), testMain(2, "tool_denied", `,"tool_use_id":"c","message":"no"`), testTask(3, "background_task_started", `,"tool_call_id":"c"`), testTask(4, "background_task_updated", `,"status":"completed"`)})
	testShell(t, f, 1, "denied", false, 2)
}
func TestShellIdentityAndValidation(t *testing.T) {
	t.Parallel()
	for _, marker := range []string{"truncated_fields", "dropped_fields"} {
		entries := []history.Entry{testShellCall(1), testTask(2, "background_task_started", `,"tool_call_id":"c","`+marker+`":["tool_call_id"]`), testTask(3, "background_task_updated", `,"status":"completed"`), testTask(4, "background_task_started", `,"tool_call_id":"c"`)}
		f := testMainReplay(t, entries)
		item := testShell(t, f, 1, "finished", false, 3)
		item.Content[0] = 'x'
		testShell(t, f, 1, "finished", false, 3)
	}
	entries := []history.Entry{testAgentCall(1), testMain(2, "tool_use", `,"tool_use_id":"shell","name":"Bash","parent_tool_use_id":"c","input":{"command":"child"}`), testEntry(3, "background_task_started", `{"task_id":"task","tool_call_id":"shell"}`), testMain(4, "turn_end", `,"stop_reason":"end_turn"`), testTask(5, "background_task_updated", `,"status":"completed"`)}
	f := testMainReplay(t, entries)
	if len(f.Items()) != 3 {
		t.Fatal(f.Items())
	}
	child := f.Items()[1]
	if child.Kind != "tool_call" || child.Parent != 1 || child.Status != "finished" || child.EndedOrder != 5 {
		t.Fatal(child)
	}
	for _, kind := range []string{"codex", "none", "unknown"} {
		entries := []history.Entry{testShellCall(1), testTask(2, "background_task_started", `,"tool_call_id":"c"`), testTask(3, "background_task_updated", `,"status":"completed"`)}
		for i := range entries {
			entries[i].Session = testSource(kind, "s")
		}
		testContent(t, testShell(t, testMainReplay(t, entries), 1, "running", true, 0), "task_id", nil)
	}
}

func TestShellFirstLifetimePendingCreation(t *testing.T) {
	t.Parallel()
	for _, order := range [][]int{{0, 1, 2, 3}, {0, 2, 1, 3}, {1, 0, 2, 3}, {1, 2, 0, 3}} {
		facts := []history.Entry{testMain(0, "tool_result", `,"tool_use_id":"c","is_error":false,"result_summary":"early"`), testDurable(0, "background_task_observed", `,"task_id":"task"`), testShellCall(0), testDurable(0, "background_task_linked", `,"tool_call_id":"c","task_id":"task"`)}
		var entries []history.Entry
		var creation uint64
		for _, n := range order {
			e := facts[n]
			e.ID = uint64(len(entries) + 1)
			entries = append(entries, e)
			if n == 2 {
				creation = e.ID
			}
		}
		f := testMainReplay(t, entries)
		item := testShell(t, f, creation, "running", true, 0)
		var data map[string]json.RawMessage
		_ = json.Unmarshal(item.Content, &data)
		if data["result"] == nil {
			t.Fatal("early result lost", string(item.Content))
		}
	}
	// A shell's lifecycle adoption must not replace its recorded parent group.
	entries := []history.Entry{testAgentCall(1), testMain(2, "tool_use", `,"tool_use_id":"shell","name":"Bash","parent_tool_use_id":"c"`), testDurable(3, "background_task_observed", `,"task_id":"task"`), testDurable(4, "background_task_linked", `,"tool_call_id":"shell","task_id":"task"`)}
	f := testMainReplay(t, entries)
	if len(f.Items()) != 2 || f.Items()[1].Parent != 1 || !f.Items()[1].Active {
		t.Fatal("lifetime adoption lost child", f.Items())
	}
}

func TestShellInvalidPendingEvidence(t *testing.T) {
	t.Parallel()
	for _, extra := range []string{`,"tool_call_id":"c","truncated_fields":["task_id"]`, `,"tool_call_id":"c","dropped_fields":["task_id"]`, `,"tool_call_id":"c","conversation_id":"foreign"`} {
		entries := []history.Entry{testTask(1, "background_task_started", extra), testTask(2, "background_task_updated", `,"status":"completed","task_id":"other"`), testShellCall(3)}
		testContent(t, testShell(t, testMainReplay(t, entries), 3, "running", true, 0), "task_id", nil)
	}
	f := testMainReplay(t, []history.Entry{testTask(1, "background_task_started", `,"tool_call_id":"c"`), testTask(2, "background_task_updated", `,"status":"completed"`)})
	if len(f.Items()) != 0 {
		t.Fatal("task invented call", f.Items())
	}
	for _, result := range []history.Entry{testEntry(2, "tool_result", `{"tool_use_id":"c","is_error":true}`), testEntry(2, "tool_result", `{"turn_id":"other","tool_use_id":"c","is_error":true}`)} {
		entries := []history.Entry{testShellCall(1), result, testTask(3, "background_task_started", `,"tool_call_id":"c"`)}
		testContent(t, testShell(t, testMainReplay(t, entries), 1, "running", true, 0), "result", nil)
	}
}

func TestShellUnlinkedChildAfterLifetime(t *testing.T) {
	t.Parallel()
	entries := []history.Entry{testAgentCall(1), testMain(2, "tool_use", `,"tool_use_id":"shell","name":"Bash","parent_tool_use_id":"c"`), testDurable(3, "background_task_observed", `,"task_id":"unrelated"`), testMain(4, "tool_result", `,"tool_use_id":"shell","parent_tool_use_id":"c","is_error":false`)}
	f := testMainReplay(t, entries)
	if len(f.Items()) != 2 || f.Items()[1].Parent != 1 || f.Items()[1].Status != "done" || f.Items()[1].Active {
		t.Fatal("unlinked child changed", f.Items())
	}
}

func TestShellRecordedIdentityAndSavedEnding(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"Bash", "local_bash"} {
		use := testShellCall(1)
		use.Payload = []byte(strings.ReplaceAll(string(use.Payload), "Bash", name))
		use.Session = testSource("claude", "original")
		use.Shown = new(bool)
		entries := []history.Entry{use, testDurable(2, "background_task_observed", `,"task_id":"task"`), testDurable(3, "background_task_linked", `,"task_id":"task","tool_call_id":"c"`), testDurable(4, "agent_ended_with_session", `,"task_id":"task","tool_call_id":"c","cause":"child_exit"`), testTask(5, "background_task_updated", `,"status":"completed"`)}
		for i := 1; i < len(entries); i++ {
			entries[i].Session = use.Session
		}
		f := testMainReplay(t, entries)
		item := testShell(t, f, 1, "ended_with_session", false, 4)
		if item.Shown || item.Turn != "t" || item.Agent != "claude" || item.Session != "original" || item.Rev != 4 {
			t.Fatal(item)
		}
		var content map[string]json.RawMessage
		_ = json.Unmarshal(item.Content, &content)
		var ending struct{ Cause string }
		_ = json.Unmarshal(content["ending"], &ending)
		if ending.Cause != "child_exit" {
			t.Fatal("saved cause replaced")
		}
		entries[0].Payload[0] = 'x'
		item.Content[0] = 'x'
		testShell(t, f, 1, "ended_with_session", false, 4)
	}
	for _, marker := range []string{"truncated_fields", "dropped_fields"} {
		bad := testDurable(2, "background_task_observed", `,"task_id":"task","`+marker+`":["lifetime_id"]`)
		testAgentNeutralFact(t, []history.Entry{testShellCall(1)}, bad)
	}
}

func TestShellInvalidEndingBeforeFirstLifetime(t *testing.T) {
	t.Parallel()
	initial := []history.Entry{testShellCall(1), testTask(2, "background_task_started", `,"tool_call_id":"c"`)}
	bad := testDurable(3, "agent_ended_with_session", `,"task_id":"task","tool_call_id":"foreign","cause":"daemon_restart"`)
	testAgentNeutralFact(t, initial, bad)
}

func TestShellTaggedBoundaryReuse(t *testing.T) {
	t.Parallel()
	entries := []history.Entry{testShellCall(1), testTask(2, "background_task_started", `,"tool_call_id":"c"`), testDivider(3, "idle_sleep", "s", "s", "claude", "claude"), testShellCall(4), testTask(5, "background_task_started", `,"tool_call_id":"c"`), testTask(6, "background_task_updated", `,"status":"completed"`)}
	for i := range entries {
		if i != 2 {
			entries[i].Session = testSource("claude", "s")
		}
	}
	f := testMainReplay(t, entries)
	testShell(t, f, 1, "running", true, 0)
	testShell(t, f, 4, "finished", false, 6)
}
