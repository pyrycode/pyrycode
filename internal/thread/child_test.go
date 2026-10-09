package thread

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
)

func testChild(id uint64, typ, parent, extra string) history.Entry {
	return testMain(id, typ, `,"parent_tool_use_id":"`+parent+`"`+extra)
}
func testChildItem(t *testing.T, f *Fold, id, parent uint64, kind, status string, active bool) Item {
	t.Helper()
	for _, item := range f.Items() {
		if item.ID == id {
			if item.Order != id || item.Parent != parent || item.Kind != kind || item.Status != status || item.Active != active {
				t.Fatalf("child: %#v", item)
			}
			return item
		}
	}
	t.Fatalf("missing child %d: %#v", id, f.Items())
	return Item{}
}
func TestChildLanes(t *testing.T) {
	t.Parallel()
	entries := []history.Entry{
		testMain(1, "tool_use", `,"tool_use_id":"a","name":"Agent"`),
		testMain(2, "tool_use", `,"tool_use_id":"b","name":"Agent"`),
		testChild(3, "assistant_delta", "a", `,"text":"child "`),
		testMain(4, "assistant_delta", `,"text":"main "`),
		testChild(5, "assistant_delta", "b", `,"text":"other"`),
		testChild(6, "assistant_delta", "a", `,"text":"text"`),
		testChild(7, "tool_use", "a", `,"tool_use_id":"read","name":"Read","input":{"path":"alpha"}`),
		testMain(8, "assistant_delta", `,"text":"reply"`),
		testChild(9, "tool_result", "a", `,"tool_use_id":"read","is_error":false,"result_detail":"marker"`),
		testChild(10, "tool_denied", "a", `,"tool_use_id":"read"`),
		testChild(11, "assistant_delta", "a", `,"text":"next"`),
		testChild(12, "turn_end", "a", `,"stop_reason":"end_turn"`),
		testMain(13, "tool_use", `,"tool_use_id":"main","name":"Read"`),
		testChild(14, "tool_use", "b", `,"tool_use_id":"pending","name":"Write"`),
		testMain(15, "turn_end", `,"stop_reason":"end_turn"`),
		testChild(16, "turn_end", "b", `,"stop_reason":"cancelled"`),
		testEntry(17, "assistant_delta", `{"turn_id":"another","parent_tool_use_id":"b","text":"different turn"}`),
	}
	f := testMainReplay(t, entries)
	testContent(t, testChildItem(t, f, 3, 1, "assistant_message", "done", false), "text", "child text")
	testContent(t, testChildItem(t, f, 4, 0, "assistant_message", "done", false), "text", "main reply")
	testChildItem(t, f, 5, 2, "assistant_message", "done", false)
	call := testChildItem(t, f, 7, 1, "tool_call", "done", false)
	testContent(t, call, "input", map[string]any{"path": "alpha"})
	testChildItem(t, f, 11, 1, "assistant_message", "done", false)
	testContent(t, testChildItem(t, f, 11, 1, "assistant_message", "done", false), "ending", map[string]any{"turn_id": "t", "parent_tool_use_id": "a", "stop_reason": "end_turn"})
	testChildItem(t, f, 13, 0, "tool_call", "interrupted", false)
	testChildItem(t, f, 14, 2, "tool_call", "interrupted", false)
	testChildItem(t, f, 17, 2, "assistant_message", "running", true)
	for _, item := range f.Items() {
		if item.Kind == "turn_end" && item.ID != 15 {
			t.Fatal("child ending row", item)
		}
	}
	snapshot := f.Items()
	snapshot[0].Content[0] = 'X'
	if reflect.DeepEqual(snapshot, f.Items()) {
		t.Fatal("snapshot aliases fold")
	}
}

func TestChildLateRepairAndClosure(t *testing.T) {
	t.Parallel()
	entries := []history.Entry{
		testMain(1, "tool_use", `,"tool_use_id":"a","name":"Agent"`),
		testMain(2, "tool_use", `,"tool_use_id":"b","name":"Task"`),
		testChild(3, "tool_use", "a", `,"tool_use_id":"c","name":"Read","input":{"path":"original"}`),
		testChild(4, "tool_result", "b", `,"tool_use_id":"c","is_error":false`),
		testChild(5, "turn_end", "a", `,"stop_reason":"end_turn"`),
	}
	entries[2].Shown = new(bool)
	for i := range entries {
		entries[i].Session = testSource("claude", "s")
	}
	f := testMainReplay(t, entries)
	item := testChildItem(t, f, 3, 2, "tool_call", "done", false)
	if item.Rev != 4 || item.Shown {
		t.Fatal("repair changed creation visibility or revision", item)
	}
	testContent(t, item, "input", map[string]any{"path": "original"})
	var content map[string]json.RawMessage
	_ = json.Unmarshal(item.Content, &content)
	if content["ending"] != nil {
		t.Fatal("old parent lane ended repaired call")
	}
}

func TestChildMalformedNeutrality(t *testing.T) {
	t.Parallel()
	initial := []history.Entry{
		testMain(1, "tool_use", `,"tool_use_id":"a","name":"Agent"`),
		testChild(2, "assistant_delta", "a", `,"text":"child"`),
		testChild(3, "tool_use", "a", `,"tool_use_id":"c","name":"Read"`),
		testChild(4, "assistant_delta", "a", `,"text":"next"`),
		testMain(5, "tool_denied", `,"tool_use_id":"a"`),
	}
	for _, extra := range []string{
		`,"text":null`, `,"text":"ignored","parent_tool_use_id":null`,
		`,"text":"ignored","truncated_fields":["parent_tool_use_id"]`,
		`,"text":"ignored","conversation_id":"foreign"`,
	} {
		testAgentNeutralFact(t, initial, testChild(6, "assistant_delta", "a", extra))
	}
}

func TestChildNestedLinkRepair(t *testing.T) {
	t.Parallel()
	entries := []history.Entry{
		testDurable(1, "agent_call_observed", `,"tool_call_id":"a","tool":"Agent"`),
		testMain(2, "tool_use", `,"tool_use_id":"a","name":"Agent"`),
		testMain(3, "tool_use", `,"tool_use_id":"b","name":"Agent"`),
		testChild(4, "tool_use", "a", `,"tool_use_id":"nested","name":"Task"`),
		testChild(5, "assistant_delta", "a", `,"text":"original lane"`),
		testChild(6, "assistant_delta", "b", `,"text":"repaired lane"`),
		testDurable(7, "background_task_linked", `,"tool_call_id":"nested","task_id":"task","parent_tool_call_id":"b"`),
		testMain(8, "tool_use", `,"tool_use_id":"nested","name":"Task"`),
	}
	f := testMainReplay(t, entries)
	nested := testChildItem(t, f, 4, 3, "agent", "running", true)
	if nested.Rev != 7 {
		t.Fatal("nested repair revision", nested)
	}
	testChildItem(t, f, 5, 2, "assistant_message", "running", true)
	testChildItem(t, f, 6, 3, "assistant_message", "done", false)
}

func TestChildMainIndependenceAndSend(t *testing.T) {
	t.Parallel()
	entries := []history.Entry{
		testDurable(1, "agent_call_observed", `,"tool_call_id":"a","tool":"Agent","parent_tool_call_id":"outer"`),
		testMain(2, "tool_use", `,"tool_use_id":"a","name":"Agent"`),
		testChild(3, "tool_use", "a", `,"tool_use_id":"c","name":"Read"`),
		testMain(4, "main_turn_opened", `,"occurred_at":"2026-01-01T00:00:00Z"`),
		testMain(5, "assistant_delta", `,"text":"main"`),
		testMain(6, "tool_denied", `,"tool_use_id":"c"`),
		testAcceptance(7, "phone"),
		testChild(8, "turn_end", "a", `,"stop_reason":"end_turn"`),
		testEntry(9, "message", `{"role":"user","text":"delivered"}`),
		testSendOutcome(10, "send_delivered", `,"accepted_entry_id":7,"delivery_entry_id":9`, "delivered"),
		testMain(11, "assistant_delta", `,"text":"next"`),
		testMain(12, "turn_end", `,"stop_reason":"end_turn"`),
		testChild(13, "assistant_delta", "absent", `,"text":"after"`),
	}
	f := New("a")
	testFeed(t, f, entries[:3]...)
	if len(f.openings) != 0 || len(f.turns) != 0 {
		t.Fatal("child traffic opened a main turn")
	}
	testFeed(t, f, entries[3:]...)
	testMainItem(t, f, 5, 9, "assistant_message", "done", false)
	testMainItem(t, f, 11, 12, "assistant_message", "done", false)
	testSendItem(t, f, 7, 9, 10, "delivered", false, true)
	if len(f.Items()) != 4 || len(f.openings) != 1 {
		t.Fatal("child traffic escaped into main work", f.Items())
	}
	testMainReplay(t, entries)
}

func TestChildTurnEndBeforeCreation(t *testing.T) {
	t.Parallel()
	entries := []history.Entry{
		testMain(1, "tool_use", `,"tool_use_id":"a","name":"Agent"`),
		testChild(2, "turn_end", "a", `,"stop_reason":"cancelled"`),
		testChild(3, "tool_result", "a", `,"tool_use_id":"c","is_error":false`),
		testChild(4, "tool_use", "a", `,"tool_use_id":"c","name":"Read"`),
	}
	f := testMainReplay(t, entries)
	item := testChildItem(t, f, 4, 1, "tool_call", "interrupted", false)
	testContent(t, item, "ending", map[string]any{"turn_id": "t", "parent_tool_use_id": "a", "stop_reason": "cancelled"})
}

func TestChildEarlyResultAfterBoundary(t *testing.T) {
	t.Parallel()
	entries := []history.Entry{
		testDivider(1, "claude_clear", "old", "s", "claude", "claude"),
		testMain(2, "tool_use", `,"tool_use_id":"a","name":"Agent"`),
		testMain(3, "tool_result", `,"tool_use_id":"c","is_error":false`),
		testChild(4, "tool_use", "a", `,"tool_use_id":"c","name":"Read"`),
	}
	for i := 1; i < len(entries); i++ {
		entries[i].Session = testSource("claude", "s")
	}
	f := testMainReplay(t, entries)
	testChildItem(t, f, 4, 2, "tool_call", "done", false)
}

func TestChildEarlyReportInPreviousMainTurn(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ typ, extra, status, field string }{
		{"tool_result", `,"is_error":false,"result_detail":"early"`, "done", "result"},
		{"tool_result", `,"is_error":true,"result_detail":"early"`, "failed", "result"},
		{"tool_denied", `,"message":"early"`, "denied", "denial"},
	} {
		for _, source := range []*history.SessionProvenance{nil, testSource("claude", "s")} {
			t.Run(tc.status+"/"+sourceName(source), func(t *testing.T) {
				entries := []history.Entry{
					testMain(1, "tool_use", `,"tool_use_id":"a","name":"Agent"`),
					testMain(2, tc.typ, `,"tool_use_id":"c"`+tc.extra),
					testMain(3, "turn_end", `,"stop_reason":"end_turn"`),
					testMain(4, "main_turn_opened", `,"occurred_at":"2026-10-09T00:00:00Z"`),
					testMain(5, "tool_result", `,"tool_use_id":"c","is_error":true,"result_detail":"conflict"`),
					testChild(6, "tool_use", "a", `,"tool_use_id":"c","name":"Read","input":{"path":"original"}`),
				}
				for i := range entries {
					entries[i].Session = source
				}
				entries[5].Shown = new(bool)
				full := testMainReplay(t, entries)
				f := New("a")
				for _, entry := range entries {
					entry.Payload = append(json.RawMessage(nil), entry.Payload...)
					testFeed(t, f, entry)
					entry.Payload[0] = 'X'
				}
				if !reflect.DeepEqual(full.Items(), f.Items()) || full.Version() != f.Version() {
					t.Fatal("single-entry feeds or input mutation changed replay")
				}
				item := testChildItem(t, f, 6, 1, "tool_call", tc.status, false)
				if item.Rev != 6 || item.Shown {
					t.Fatal("early report changed creation revision or visibility", item)
				}
				testContent(t, item, "input", map[string]any{"path": "original"})
				var report map[string]any
				if err := json.Unmarshal(entries[1].Payload, &report); err != nil {
					t.Fatal(err)
				}
				testContent(t, item, tc.field, report)
				testContent(t, item, "ending", nil)
			})
		}
	}
}

func TestChildNewLifetimeEarlyReport(t *testing.T) {
	t.Parallel()
	for _, source := range []*history.SessionProvenance{nil, testSource("claude", "s")} {
		t.Run(sourceName(source), func(t *testing.T) {
			entries := []history.Entry{
				testDurable(1, "agent_call_observed", `,"tool_call_id":"a","tool":"Agent"`),
				testMain(2, "tool_use", `,"tool_use_id":"a","name":"Agent"`),
				testMain(3, "tool_result", `,"tool_use_id":"c","is_error":true,"result_detail":"old"`),
				testDurable(4, "agent_call_observed", `,"lifetime_id":"22222222-2222-4222-8222-222222222222","tool_call_id":"a","tool":"Agent"`),
				testMain(5, "tool_use", `,"tool_use_id":"a","name":"Agent"`),
				testMain(6, "tool_result", `,"tool_use_id":"c","is_error":false,"result_detail":"new"`),
				testMain(7, "tool_denied", `,"tool_use_id":"c","message":"conflict"`),
				testChild(8, "tool_use", "a", `,"tool_use_id":"c","name":"Read","input":{"path":"new"}`),
			}
			for i := range entries {
				entries[i].Session = source
			}
			f := testMainReplay(t, entries)
			item := testChildItem(t, f, 8, 5, "tool_call", "done", false)
			if item.Rev != 8 {
				t.Fatal("early report revision precedes creation", item)
			}
			testContent(t, item, "input", map[string]any{"path": "new"})
			testContent(t, item, "result", map[string]any{"turn_id": "t", "tool_use_id": "c", "is_error": false, "result_detail": "new"})
			testContent(t, item, "denial", nil)
			testChildItem(t, f, 2, 0, "agent", "running", true)
			testChildItem(t, f, 5, 0, "agent", "running", true)
		})
	}
}

func TestChildLifetimeReuse(t *testing.T) {
	t.Parallel()
	entries := []history.Entry{
		testDurable(1, "agent_call_observed", `,"tool_call_id":"a","tool":"Agent"`),
		testMain(2, "tool_use", `,"tool_use_id":"a","name":"Agent"`),
		testChild(3, "assistant_delta", "a", `,"text":"old"`),
		testMain(4, "tool_result", `,"tool_use_id":"c","is_error":true`),
		testDurable(5, "agent_call_observed", `,"tool_call_id":"a","tool":"Agent"`),
		testMain(6, "tool_use", `,"tool_use_id":"a","name":"Agent"`),
		testChild(7, "assistant_delta", "a", `,"text":"new"`),
		testChild(8, "tool_use", "a", `,"tool_use_id":"c","name":"Read"`),
		testChild(9, "turn_end", "a", `,"stop_reason":"end_turn"`),
	}
	entries[4] = testDurable(5, "agent_call_observed", `,"lifetime_id":"22222222-2222-4222-8222-222222222222","tool_call_id":"a","tool":"Agent"`)
	f := testMainReplay(t, entries)
	testContent(t, testChildItem(t, f, 3, 2, "assistant_message", "running", true), "text", "old")
	testContent(t, testChildItem(t, f, 7, 6, "assistant_message", "done", false), "text", "new")
	testChildItem(t, f, 8, 6, "tool_call", "interrupted", false)
}
func TestChildRepair(t *testing.T) {
	t.Parallel()
	entries := []history.Entry{
		testMain(1, "tool_use", `,"tool_use_id":"a","name":"Agent"`),
		testChild(2, "tool_result", "missing", `,"tool_use_id":"c","is_error":true,"result_detail":"early"`),
		testMain(3, "assistant_delta", `,"text":"main"`),
		testMain(4, "tool_use", `,"tool_use_id":"c","name":"Read","input":{"path":"original"}`),
		testChild(5, "tool_denied", "a", `,"tool_use_id":"c"`),
		testMain(6, "assistant_delta", `,"text":" continues"`),
	}
	f := New("a")
	testFeed(t, f, entries[:4]...)
	if len(f.Items()) != 2 {
		t.Fatal("unresolved child escaped", f.Items())
	}
	testFeed(t, f, entries[4:]...)
	call := testChildItem(t, f, 4, 1, "tool_call", "failed", false)
	if call.Rev != 5 {
		t.Fatal("repair revision", call)
	}
	testContent(t, call, "input", map[string]any{"path": "original"})
	testContent(t, testChildItem(t, f, 3, 0, "assistant_message", "running", true), "text", "main continues")
	testMainReplay(t, entries)
}
func TestChildNested(t *testing.T) {
	t.Parallel()
	entries := []history.Entry{
		testMain(1, "tool_use", `,"tool_use_id":"a","name":"Agent"`),
		testMain(2, "tool_use", `,"tool_use_id":"b","name":"Agent"`),
		testChild(3, "tool_use", "missing", `,"tool_use_id":"nested","name":"Task"`),
		testChild(4, "assistant_delta", "nested", `,"text":"deep"`),
		testChild(5, "tool_result", "a", `,"tool_use_id":"nested","is_error":false`),
		testMain(6, "background_task_started", `,"task_id":"task","tool_call_id":"nested"`),
		testChild(7, "tool_use", "nested", `,"tool_use_id":"read","name":"Read"`),
		testMain(8, "tool_denied", `,"tool_use_id":"a"`),
	}
	f := testMainReplay(t, entries)
	nested := testChildItem(t, f, 3, 1, "agent", "running", false)
	if nested.EndedOrder != 0 {
		t.Fatal("ancestor invented completion")
	}
	testChildItem(t, f, 4, 3, "assistant_message", "done", false)
	testChildItem(t, f, 7, 3, "tool_call", "interrupted", false)
	testFeed(t, f, testChild(9, "tool_result", "b", `,"tool_use_id":"nested","is_error":false`))
	testChildItem(t, f, 3, 2, "agent", "running", true)
	testChildItem(t, f, 4, 3, "assistant_message", "done", false)
}
func TestChildReclassification(t *testing.T) {
	t.Parallel()
	entries := []history.Entry{
		testMain(1, "tool_use", `,"tool_use_id":"a","name":"Agent"`),
		testChild(2, "assistant_delta", "a", `,"text":"working"`),
		testChild(3, "tool_use", "a", `,"tool_use_id":"c","name":"Read"`),
		testChild(4, "tool_use", "a", `,"tool_use_id":"d","name":"Write"`),
		testChild(5, "assistant_delta", "a", `,"text":"still working"`),
		testMain(6, "tool_result", `,"tool_use_id":"a","is_error":false`),
		testChild(7, "tool_result", "a", `,"tool_use_id":"c","is_error":true`),
		testMain(8, "background_task_started", `,"task_id":"task","tool_call_id":"a"`),
		testMain(9, "background_task_updated", `,"task_id":"task","status":"stopped"`),
	}
	f := New("a")
	testFeed(t, f, entries[:6]...)
	testChildItem(t, f, 3, 1, "tool_call", "interrupted", false)
	testChildItem(t, f, 4, 1, "tool_call", "interrupted", false)
	testChildItem(t, f, 5, 1, "assistant_message", "interrupted", false)
	testFeed(t, f, entries[6:8]...)
	testChildItem(t, f, 3, 1, "tool_call", "failed", false)
	testChildItem(t, f, 4, 1, "tool_call", "running", true)
	text := testChildItem(t, f, 5, 1, "assistant_message", "running", true)
	testContent(t, text, "parent_ending", nil)
	if text.Rev != 8 {
		t.Fatal("reopening revision", text)
	}
	testFeed(t, f, entries[8:]...)
	testChildItem(t, f, 3, 1, "tool_call", "failed", false)
	testChildItem(t, f, 4, 1, "tool_call", "interrupted", false)
	testChildItem(t, f, 5, 1, "assistant_message", "interrupted", false)
	testMainReplay(t, entries)
}
func TestChildScopes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, parent string
		source       *history.SessionProvenance
	}{
		{"self", "c", nil}, {"newer", "later", nil}, {"ordinary", "ordinary", nil},
		{"source", "a", testSource("claude", "other")}, {"codex", "a", testSource("codex", "s")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entries := []history.Entry{testMain(1, "tool_use", `,"tool_use_id":"a","name":"Agent"`), testMain(2, "tool_use", `,"tool_use_id":"ordinary","name":"Read"`), testChild(3, "tool_use", tc.parent, `,"tool_use_id":"c","name":"Read"`), testMain(4, "tool_use", `,"tool_use_id":"later","name":"Agent"`)}
			entries[2].Session = tc.source
			if tc.name == "self" {
				entries[2] = testChild(3, "tool_use", "c", `,"tool_use_id":"c","name":"Agent"`)
			}
			f := testMainReplay(t, entries)
			for _, item := range f.Items() {
				if item.ID == 3 {
					t.Fatal("invalid parent joined", item)
				}
			}
		})
	}
	entries := []history.Entry{testMain(1, "tool_use", `,"tool_use_id":"a","name":"Agent"`), testChild(2, "assistant_delta", "absent", `,"text":"pending"`), testDivider(3, "claude_clear", "s", "n", "claude", "claude"), testMain(4, "tool_use", `,"tool_use_id":"absent","name":"Agent"`)}
	f := testMainReplay(t, entries)
	for _, item := range f.Items() {
		if item.ID == 2 {
			t.Fatal("scope stolen", item)
		}
	}
	// Identity-loss facts must not change retained joins or public items.
	before := f.Items()
	bad := testChild(5, "tool_use", "a", `,"tool_use_id":"bad","name":"Read","truncated_fields":["parent_tool_use_id"]`)
	testFeed(t, f, bad)
	if !reflect.DeepEqual(before, f.Items()) || f.Version() != 5 {
		t.Fatal("malformed changed items")
	}
}
