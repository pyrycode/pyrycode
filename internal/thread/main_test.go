package thread

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
)

func testMain(id uint64, typ, extra string) history.Entry {
	return testEntry(id, typ, `{"turn_id":"t"`+extra+`}`)
}
func testMainReplay(t *testing.T, entries []history.Entry) *Fold {
	t.Helper()
	full := New("a")
	testFeed(t, full, entries...)
	for split := 0; split <= len(entries); split++ {
		f := New("a")
		testFeed(t, f, entries[:split]...)
		testFeed(t, f, entries[split:]...)
		if !reflect.DeepEqual(full.Items(), f.Items()) || full.Version() != f.Version() {
			t.Fatalf("partition %d differs from replay", split)
		}
	}
	return full
}
func testMainItem(t *testing.T, f *Fold, id, rev uint64, kind, status string, active bool) Item {
	t.Helper()
	for _, item := range f.Items() {
		if item.ID == id {
			if item.Order != id || item.Rev != rev || item.Kind != kind || item.Status != status || item.Active != active || item.Parent != 0 {
				t.Fatalf("item %d: %#v", id, item)
			}
			return item
		}
	}
	t.Fatalf("missing item %d in %#v", id, f.Items())
	return Item{}
}
func testContent(t *testing.T, item Item, key string, want any) {
	t.Helper()
	var fields map[string]any
	if err := json.Unmarshal(item.Content, &fields); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fields[key], want) {
		t.Fatalf("%s: got %#v want %#v", key, fields[key], want)
	}
}

func TestMainTextRuns(t *testing.T) {
	t.Parallel()
	for _, source := range []*history.SessionProvenance{nil, testSource("claude", "s"), testSource("codex", "s")} {
		t.Run(sourceName(source), func(t *testing.T) {
			entries := []history.Entry{
				testMain(1, "main_turn_opened", `,"occurred_at":"2026-01-01T00:00:00Z"`),
				testMain(2, "assistant_delta", `,"text":"hello\n","seq":1,"future":"saved"`),
				testMain(3, "send_accepted", `,"text":"queued"`),
				testMain(4, "assistant_delta", `,"text":"child","parent_tool_use_id":"agent"`),
				testMain(5, "tool_use", `,"tool_use_id":"child","name":"Bash","parent_tool_use_id":"agent"`),
				testMain(6, "assistant_delta", `,"text":"world","seq":2`),
				testMain(7, "tool_use", `,"tool_use_id":"c","name":"Read","input":{"path":"file"}`),
				testMain(8, "assistant_delta", `,"text":"next"`),
				testMessage(9),
				testMain(10, "assistant_delta", `,"text":"last"`),
				testMain(11, "turn_end", `,"stop_reason":"end_turn","outcome":"success","input_tokens":42`),
				testMain(12, "assistant_delta", `,"text":"late"`),
			}
			for i := range entries {
				entries[i].Session = source
			}
			f := testMainReplay(t, entries)
			if len(f.Items()) != 8 {
				t.Fatalf("unexpected rows: %#v", f.Items())
			}
			first := testMainItem(t, f, 2, 7, "assistant_message", "done", false)
			testContent(t, first, "text", "hello\nworld")
			testContent(t, first, "future", "saved")
			if first.Summary != "hello world" || first.Turn != "t" {
				t.Fatal(first)
			}
			testMainItem(t, f, 7, 11, "tool_call", "interrupted", false)
			testMainItem(t, f, 8, 9, "assistant_message", "done", false)
			testMainItem(t, f, 10, 11, "assistant_message", "done", false)
			end := testMainItem(t, f, 11, 11, "turn_end", "done", false)
			if end.Shown {
				t.Fatal("successful end shown")
			}
			testContent(t, end, "input_tokens", float64(42))
			testMainItem(t, f, 12, 12, "assistant_message", "done", false)
			if source != nil && (first.Agent != source.Kind || first.Session != "s") {
				t.Fatal(first)
			}
		})
	}
}

func TestMainToolTerminals(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ typ, body, status, field string }{
		{"tool_result", `,"tool_use_id":"c","is_error":false,"result_summary":"ok","result_detail":"detail"`, "done", "result"},
		{"tool_result", `,"tool_use_id":"c","is_error":true,"result_detail":"failed"`, "failed", "result"},
		{"tool_denied", `,"tool_use_id":"c","tool_name":"Bash","message":"no","decision_reason":"rule"`, "denied", "denial"},
		{"main_tool_interrupted", `,"tool_call_id":"c","cause":"child_exit","occurred_at":"2026-01-01T00:00:00Z"`, "interrupted", "interruption"},
	} {
		for _, source := range []*history.SessionProvenance{nil, testSource("claude", "s"), testSource("codex", "s")} {
			for _, early := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/%s/early=%t", tc.typ, tc.status, sourceName(source), early), func(t *testing.T) {
					use := testMain(1, "tool_use", `,"tool_use_id":"c","name":"Bash","input":{"command":"pwd"},"input_summary":"pwd"`)
					terminal := testMain(2, tc.typ, tc.body)
					entries := []history.Entry{use, terminal}
					creation, rev := uint64(1), uint64(2)
					if early {
						terminal.ID, use.ID = 1, 2
						entries = []history.Entry{terminal, use}
						creation = 2
					}
					entries = append(entries, testMain(3, "tool_result", `,"tool_use_id":"c","is_error":true,"result_detail":"conflict"`), testMain(4, "tool_denied", `,"tool_use_id":"c","message":"duplicate"`), testMain(5, "tool_use", `,"tool_use_id":"c","name":"Write"`))
					for i := range entries {
						entries[i].Session = source
					}
					f := testMainReplay(t, entries)
					if len(f.Items()) != 1 {
						t.Fatal(f.Items())
					}
					item := testMainItem(t, f, creation, rev, "tool_call", tc.status, false)
					testContent(t, item, "input", map[string]any{"command": "pwd"})
					var want map[string]any
					if err := json.Unmarshal(terminal.Payload, &want); err != nil {
						t.Fatal(err)
					}
					testContent(t, item, tc.field, want)
				})
			}
		}
	}
}

func TestMainEndings(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		typ, body, status string
		shown             bool
	}{
		{"turn_end", `,"stop_reason":"end_turn"`, "done", false},
		{"turn_end", `,"stop_reason":"end_turn","is_error":true`, "failed", true},
		{"turn_end", `,"stop_reason":"cancelled"`, "done", true},
		{"main_turn_interrupted", `,"cause":"daemon_restart","occurred_at":"2026-01-01T00:00:00Z"`, "interrupted", true},
	} {
		t.Run(tc.typ+tc.body, func(t *testing.T) {
			entries := []history.Entry{testMain(1, tc.typ, tc.body), testMain(2, "main_turn_opened", `,"occurred_at":"2026-01-01T00:00:00Z"`), testMain(3, "assistant_delta", `,"text":"late"`), testMain(4, "tool_use", `,"tool_use_id":"c","name":"Read"`), testMain(5, "tool_result", `,"tool_use_id":"c","is_error":false`)}
			f := testMainReplay(t, entries)
			end := testMainItem(t, f, 1, 1, "turn_end", tc.status, false)
			if end.Shown != tc.shown {
				t.Fatal(end)
			}
			testMainItem(t, f, 3, 3, "assistant_message", "done", false)
			testMainItem(t, f, 4, 4, "tool_call", "interrupted", false)
			entries[0].Shown = new(bool)
			*entries[0].Shown = !tc.shown
			if testMainReplay(t, entries).Items()[0].Shown == tc.shown {
				t.Fatal("explicit shown ignored")
			}
		})
	}
}

func TestMainScopeAndRecovery(t *testing.T) {
	t.Parallel()
	for _, explicit := range []bool{false, true} {
		for _, source := range []*history.SessionProvenance{nil, testSource("claude", "s"), testSource("codex", "s")} {
			t.Run(fmt.Sprintf("%s/explicit=%t", sourceName(source), explicit), func(t *testing.T) {
				opening := testMain(1, "assistant_delta", `,"text":"old"`)
				if explicit {
					opening = testMain(1, "main_turn_opened", `,"occurred_at":"2026-01-01T00:00:00Z"`)
				}
				entries := []history.Entry{opening, testMain(2, "tool_use", `,"tool_use_id":"c","name":"Read"`), testDivider(3, "agent_switch", "s", "n", "claude", "codex"), testMain(4, "main_turn_opened", `,"occurred_at":"2026-01-01T00:00:00Z"`), testMain(5, "tool_use", `,"tool_use_id":"c","name":"Write"`), testMain(6, "main_tool_interrupted", `,"turn_opened_entry_id":1,"tool_call_id":"c","cause":"daemon_restart","occurred_at":"2026-01-01T00:00:00Z"`), testMain(7, "main_turn_interrupted", `,"turn_opened_entry_id":1,"cause":"daemon_restart","occurred_at":"2026-01-01T00:00:00Z"`), testMain(8, "main_turn_interrupted", `,"turn_opened_entry_id":999,"cause":"daemon_restart","occurred_at":"2026-01-01T00:00:00Z"`)}
				for i := range entries {
					if i != 2 {
						entries[i].Session = source
					}
				}
				f := testMainReplay(t, entries)
				testMainItem(t, f, 2, 6, "tool_call", "interrupted", false)
				testMainItem(t, f, 5, 5, "tool_call", "running", true)
				bad := testMain(9, "main_turn_interrupted", `,"turn_opened_entry_id":4,"cause":"child_exit","occurred_at":"2026-01-01T00:00:00Z"`)
				bad.Session = testSource("codex", "other")
				testFeed(t, f, bad)
				testMainItem(t, f, 5, 5, "tool_call", "running", true)
			})
		}
	}
	// Display fallback must not join tagged work; recorded agent is part of source.
	entries := []history.Entry{testDivider(1, "agent_switch", "old", "s", "claude", "codex"), testMain(2, "tool_use", `,"tool_use_id":"c","name":"Read"`), testMain(3, "tool_result", `,"tool_use_id":"c","is_error":false`), testMain(4, "tool_result", `,"tool_use_id":"c","is_error":true`), testMain(5, "tool_result", `,"tool_use_id":"c","is_error":false`)}
	entries[2].Session = testSource("codex", "s")
	entries[3].Session = testSource("claude", "s")
	f := testMainReplay(t, entries)
	item := testMainItem(t, f, 2, 5, "tool_call", "done", false)
	if item.Session != "s" || item.Agent != "codex" {
		t.Fatal(item)
	}
}

func TestMainValidationAndLaunchers(t *testing.T) {
	t.Parallel()
	entries := []history.Entry{testMain(1, "assistant_delta", `,"text":"one"`)}
	for _, e := range []history.Entry{
		testMain(2, "tool_use", `,"tool_use_id":"child","name":"Bash","parent_tool_use_id":"a"`),
		testMain(3, "tool_result", `,"tool_use_id":"child","parent_tool_use_id":"a"`),
		testMain(4, "tool_use", `,"tool_use_id":"bad","name":"Read","input":{"path":null}`),
		testMain(5, "tool_use", `,"tool_use_id":"bad","name":"Read","parent_tool_use_id":null`),
		testMain(6, "tool_use", `,"tool_use_id":"bad","name":"Read","dropped_fields":[null]`),
		testMain(7, "turn_end", `,"stop_reason":"end_turn","is_error":null`),
		testMain(8, "tool_result", `,"tool_use_id":"bad","is_error":"true"`),
		testMain(9, "tool_use", `,"tool_use_id":"bad","name":"Read","truncated_fields":["turn_id"]`),
		testMain(10, "assistant_delta", `,"text":"foreign","conversation_id":"b"`),
		testMain(11, "tool_use", `,"tool_use_id":"","name":"Read"`),
		testMain(12, "tool_result", `,"tool_use_id":"bad","dropped_fields":["tool_use_id"]`),
	} {
		e.Shown = new(bool)
		*e.Shown = true
		entries = append(entries, e)
	}
	entries = append(entries, testMain(13, "assistant_delta", `,"text":"two"`), testMain(14, "tool_use", `,"tool_use_id":"a","name":"Agent"`), testMain(15, "tool_result", `,"tool_use_id":"a","result_detail":"launch"`), testMain(16, "assistant_delta", `,"text":"three"`), testMain(17, "tool_use", `,"tool_use_id":"task","name":"Task"`), testMain(18, "tool_result", `,"tool_use_id":"task"`), testMain(19, "assistant_delta", `,"text":"four"`))
	f := testMainReplay(t, entries)
	if len(f.Items()) != 3 {
		t.Fatal(f.Items())
	}
	testContent(t, testMainItem(t, f, 1, 14, "assistant_message", "done", false), "text", "onetwo")
	testMainItem(t, f, 16, 17, "assistant_message", "done", false)
	testMainItem(t, f, 19, 19, "assistant_message", "running", true)
}

func TestMainReplayAndOwnership(t *testing.T) {
	t.Parallel()
	e := testMain(1, "tool_result", `,"tool_use_id":"c","is_error":false,"result_detail":"original"`)
	f := New("a")
	testFeed(t, f, e)
	e.Payload[2] = 'X'
	testFeed(t, f, testMain(2, "tool_use", `,"tool_use_id":"c","name":"Read"`))
	snapshot := f.Items()
	if len(snapshot) != 1 {
		t.Fatalf("missing call: %#v", snapshot)
	}
	snapshot[0].Content[2] = 'Y'
	var fields map[string]any
	if err := json.Unmarshal(f.Items()[0].Content, &fields); err != nil {
		t.Fatal(err)
	}
	testContent(t, f.Items()[0], "result", map[string]any{"turn_id": "t", "tool_use_id": "c", "is_error": false, "result_detail": "original"})
	other := New("b")
	testFeed(t, other, testMain(1, "tool_use", `,"tool_use_id":"c","name":"Read"`))
	testMainItem(t, other, 1, 1, "tool_call", "running", true)
}

func TestMainReferenceNeutrality(t *testing.T) {
	t.Parallel()
	for _, ref := range []string{`null`, `0`, `999`, `"1"`, `1.5`} {
		t.Run(ref, func(t *testing.T) {
			entries := []history.Entry{testMain(1, "main_turn_opened", `,"occurred_at":"2026-01-01T00:00:00Z"`), testMain(2, "tool_use", `,"tool_use_id":"c","name":"Read"`), testMain(3, "main_tool_interrupted", `,"turn_opened_entry_id":`+ref+`,"tool_call_id":"c","cause":"daemon_restart","occurred_at":"2026-01-01T00:00:00Z"`), testMain(4, "main_turn_interrupted", `,"turn_opened_entry_id":`+ref+`,"cause":"daemon_restart","occurred_at":"2026-01-01T00:00:00Z"`)}
			f := testMainReplay(t, entries)
			testMainItem(t, f, 2, 2, "tool_call", "running", true)
		})
	}
	// A resolvable reference with a different turn cannot close the referenced turn.
	entries := []history.Entry{testMain(1, "tool_use", `,"tool_use_id":"c","name":"Read"`), testEntry(2, "main_turn_interrupted", `{"turn_id":"other","turn_opened_entry_id":1,"cause":"child_exit","occurred_at":"2026-01-01T00:00:00Z"}`)}
	testMainItem(t, testMainReplay(t, entries), 1, 1, "tool_call", "running", true)
}

func TestMainScopedEndAndLateTerminal(t *testing.T) {
	t.Parallel()
	entries := []history.Entry{testMain(1, "tool_use", `,"tool_use_id":"c","name":"Read"`), testMain(2, "assistant_delta", `,"text":"other source"`), testEntry(3, "assistant_delta", `{"turn_id":"other","text":"other turn"}`), testMain(4, "turn_end", `,"stop_reason":"cancelled"`), testMain(5, "tool_result", `,"tool_use_id":"c","is_error":false`), testMain(6, "tool_use", `,"tool_use_id":"late","name":"Write"`), testMain(7, "tool_result", `,"tool_use_id":"late","is_error":false`), testMain(8, "assistant_delta", `,"text":"late"`), testMain(9, "assistant_delta", `,"text":" text"`)}
	for i := range entries {
		entries[i].Session = testSource("claude", "s")
	}
	entries[1].Session = testSource("codex", "s")
	f := testMainReplay(t, entries)
	first := testMainItem(t, f, 1, 4, "tool_call", "interrupted", false)
	var fields map[string]any
	if err := json.Unmarshal(first.Content, &fields); err != nil {
		t.Fatal(err)
	}
	if _, present := fields["result"]; present {
		t.Fatal("turn closure invented successful result")
	}
	testMainItem(t, f, 2, 2, "assistant_message", "running", true)
	testMainItem(t, f, 3, 3, "assistant_message", "running", true)
	testMainItem(t, f, 6, 6, "tool_call", "interrupted", false)
	testContent(t, testMainItem(t, f, 8, 9, "assistant_message", "done", false), "text", "late text")
}

func TestMainMalformedStateNeutrality(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ typ, raw string }{
		{"assistant_delta", `,"text":"bad","seq":null`},
		{"tool_use", `,"tool_use_id":"c","name":"Read","input":{"path":null}`},
		{"tool_denied", `,"tool_use_id":"c","message":null`},
		{"tool_result", `,"tool_use_id":"c","is_error":null`},
		{"main_tool_interrupted", `,"tool_call_id":"c","cause":"child_exit","occurred_at":null`},
		{"main_turn_interrupted", `,"cause":"child_exit","occurred_at":"invalid"`},
		{"turn_end", `,"stop_reason":"end_turn","input_tokens":null`},
	} {
		t.Run(tc.typ, func(t *testing.T) {
			initial := []history.Entry{testMain(1, "assistant_delta", `,"text":"one"`), testMain(2, "tool_result", `,"tool_use_id":"c","is_error":false`)}
			f, want := New("a"), New("a")
			testFeed(t, f, initial...)
			testFeed(t, want, initial...)
			bad := testMain(3, tc.typ, tc.raw)
			bad.Session = testSource("codex", "bad")
			testFeed(t, f, bad)
			want.version = 3
			if !reflect.DeepEqual(f, want) {
				t.Fatal("malformed fact changed rows, joins or attribution")
			}
		})
	}
}
