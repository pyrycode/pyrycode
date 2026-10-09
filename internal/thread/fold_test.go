package thread

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/history"
)

func testEntry(id uint64, typ, payload string) history.Entry {
	return history.Entry{ID: id, Type: typ, Payload: json.RawMessage(payload), TS: time.Unix(int64(100-id), 0)}
}
func testFeed(t *testing.T, f *Fold, entries ...history.Entry) {
	t.Helper()
	if err := f.Feed(entries); err != nil {
		t.Fatal(err)
	}
}
func testSource(kind, id string) *history.SessionProvenance {
	return &history.SessionProvenance{Kind: kind, SessionID: id}
}
func testMessage(id uint64) history.Entry {
	return testEntry(id, "message", `{"role":"user","text":"hello"}`)
}

func TestStandaloneItems(t *testing.T) {
	t.Parallel()
	cases := []struct {
		typ, raw, kind, summary string
		shown                   bool
	}{
		{"message", `{"role":"user","text":"hello\nworld\u001b","message_id":"client","attachment_ids":["file"],"device_name":"phone","client_version":"v1","client_sent_at":"yesterday","queued_msg_id":9}`, "user_message", "hello world", true},
		{"message", `{"role":"user","text":"\u001b\t\n"}`, "user_message", "user message", true},
		{"compaction_boundary", `{"trigger":"manual","pre_tokens":0,"post_tokens":null}`, "compaction", "Context compacted", true},
		{"compaction_boundary", `{"trigger":"auto"}`, "compaction", "Context compacted", true},
		{"banner", `{"level":"info","text":"rerouted to model","stops_turn":false}`, "notice", "rerouted to model", false},
		{"banner", `{"level":"info","text":"blocked","stops_turn":true}`, "notice", "blocked", true},
		{"banner", `{"level":"warning","text":"warning"}`, "notice", "warning", true},
		{"model_refusal_fallback", `{"original_model":"a","fallback_model":"b","banner":"fallback"}`, "notice", "fallback", true},
		{"model_refusal_no_fallback", `{"original_model":"a","banner":"refused"}`, "notice", "refused", true},
		{"unrecognized_message", `{"site":"stdout","message_type":"new","raw":"odd\noutput","truncated":true}`, "notice", "Unrecognized message", true},
		{"attachment_offered", `{"attachment_id":"file","filename":"image.png"}`, "notice", "image.png", false},
		{"prompt_answered", `{"correlation_id":"q","decision":"answer","behavior":"allow","context":{"questions":[{"index":0,"text":"Which?","values":[{"text":"yes","meaning":"agree"}]}]},"truncated":false}`, "notice", "Prompt answered: answer", true},
	}
	for _, source := range []*history.SessionProvenance{nil, testSource("claude", "s"), testSource("codex", "c"), testSource("none", "")} {
		for _, tc := range cases {
			t.Run(tc.typ+"/"+tc.summary+"/"+sourceName(source), func(t *testing.T) {
				e := testEntry(7, tc.typ, tc.raw)
				e.Session = source
				f := New("a")
				testFeed(t, f, e)
				session, agent := "", ""
				none := false
				if source != nil {
					session = source.SessionID
					none = source.Kind == "none"
					if !none {
						agent = source.Kind
					}
				}
				status := "done"
				if tc.kind == "user_message" {
					status = "delivered"
				}
				subtype := ""
				if tc.kind == "notice" {
					subtype = tc.typ
				}
				want := []Item{{ID: 7, Kind: tc.kind, Order: 7, Rev: 7, Session: session, Agent: agent, NoChild: none, Status: status, Shown: tc.shown, Summary: tc.summary, Subtype: subtype, Content: json.RawMessage(tc.raw)}}
				if !reflect.DeepEqual(f.Items(), want) {
					t.Fatalf("got %#v want %#v", f.Items(), want)
				}
				shown := !tc.shown
				e.Shown = &shown
				e.ID = 8
				testFeed(t, f, e)
				if f.Items()[1].Shown != shown {
					t.Fatal("explicit visibility ignored")
				}
			})
		}
	}
}
func sourceName(p *history.SessionProvenance) string {
	if p == nil {
		return "unknown"
	}
	return p.Kind
}

func TestExcludedAndMalformed(t *testing.T) {
	t.Parallel()
	types := []string{"permission_request", "modal_shown", "question_shown", "turn_state", "stall", "api_retry", "compacting", "tool_progress", "thinking_progress", "background_task_roster", "background_task_progress", "rate_limited", "context_usage", "model_announced", "session_facts", "mcp_status", "model_list", "slash_command_list", "reply_suggestion", "session_error", "assistant_delta", "tool_use", "tool_result", "tool_denied", "turn_end", "main_turn_opened", "main_tool_interrupted", "main_turn_interrupted", "send_accepted", "send_delivered", "send_dropped", "send_lost", "agent_observed", "agent_result", "agent_denied", "task_observed", "task_linked", "task_outcome", "task_gone", "agent_session_ended", "future_kind"}
	f := New("a")
	shown := true
	for i, typ := range types {
		e := testEntry(uint64(i+1), typ, `{"text":"shown","cause":"operator_reset"}`)
		e.Shown = &shown
		testFeed(t, f, e)
	}
	bad := []history.Entry{
		testEntry(100, "message", `{"role":"assistant","text":"deferred"}`),
		testEntry(101, "message", `{"role":"user","text":4}`),
		testEntry(102, "message", `{`), testEntry(103, "message", `null`), testEntry(104, "message", `[]`),
		testEntry(105, "message", `{"role":"user"}`),
		testEntry(106, "compaction_boundary", `{"trigger":"auto","pre_tokens":"0"}`),
		testEntry(107, "banner", `{"text":"bad","stops_turn":"yes"}`),
		testEntry(108, "prompt_answered", `{"decision":"answer","context":"wrong"}`),
		testEntry(109, "session_divider", `{"cause":"future","occurred_at":"2026-01-01T00:00:00Z","previous_session_id":"s","new_session_id":"n"}`),
		testEntry(110, "session_transition", `{"reason":"future","occurred_at":"2026-01-01T00:00:00Z","previous_session_id":"s","new_session_id":"n"}`),
		testEntry(111, "session_divider", `{"cause":"operator_reset","previous_session_id":"s","new_session_id":"n"}`),
		testEntry(112, "message", `{"conversation_id":"foreign","role":"user","text":"bad"}`),
	}
	testFeed(t, f, bad...)
	if len(f.Items()) != 0 || f.Version() != 112 || f.legacyScope != 0 {
		t.Fatalf("invalid input changed state: %#v", f)
	}
	testFeed(t, f, testMessage(113))
	if f.Items()[0].Session != "" {
		t.Fatal("invalid boundary changed attribution")
	}
}

func TestFoldReplay(t *testing.T) {
	t.Parallel()
	entries := []history.Entry{testMessage(1), testDivider(2, "agent_switch", "s", "n", "", "codex"), testMessage(3), testTransition(4, "clear", "s", "n"), testMessage(5), testDivider(6, "idle_sleep", "n", "", "codex", ""), testTransition(7, "idle_evict", "n", "n"), testMessage(8)}
	entries[3].Session = testSource("codex", "n")
	full := New("a")
	testFeed(t, full, entries...)
	for split := 0; split <= len(entries); split++ {
		f := New("a")
		testFeed(t, f, entries[:split]...)
		testFeed(t, f, entries[split:]...)
		if !reflect.DeepEqual(f.Items(), full.Items()) || f.Version() != 8 || f.legacyScope != 2 {
			t.Fatalf("split %d differs", split)
		}
	}
	items := full.Items()
	if len(items) != 6 || items[1].ID != 2 || items[2].Session != "n" || items[2].Agent != "codex" || items[5].Session != "" {
		t.Fatalf("replay wrong: %#v", items)
	}
	other := New("b")
	testFeed(t, other, testMessage(1))
	if other.Version() != 1 || other.Items()[0].Session != "" {
		t.Fatal("conversation isolation")
	}
}

func TestFoldOwnership(t *testing.T) {
	t.Parallel()
	f := New("a")
	e := testMessage(1)
	e.Session = testSource("claude", "s")
	testFeed(t, f, e)
	e.Payload[2] = 'X'
	e.Session.Kind = "codex"
	snapshot := f.Items()
	snapshot[0].Content[2] = 'Y'
	snapshot[0].Kind = "bad"
	if strings.Contains(string(f.Items()[0].Content), "X") || strings.Contains(string(f.Items()[0].Content), "Y") || f.Items()[0].Agent != "claude" || f.Items()[0].Kind != "user_message" {
		t.Fatal("mutable alias")
	}
	for _, id := range []uint64{0, 1, history.MaxEntryID + 1} {
		if f.Feed([]history.Entry{testMessage(id)}) == nil {
			t.Fatalf("accepted invalid id %d", id)
		}
	}
	if f.Version() != 1 || len(f.Items()) != 1 {
		t.Fatal("invalid id mutated fold")
	}
	testFeed(t, f, testMessage(history.MaxEntryID))
	if f.Version() != history.MaxEntryID {
		t.Fatal("maximum ID lost")
	}
}
