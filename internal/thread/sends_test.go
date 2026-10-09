package thread

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
)

func testAcceptance(id uint64, device string) history.Entry {
	return testEntry(id, "send_accepted", `{"conversation_id":"a","text":"queued\ntext","attachment_ids":["queued-file"],"device_id":"`+device+`","message_id":"same","accepted_at":"2026-01-01T00:00:00Z","client_sent_at":"client time"}`)
}
func testSendOutcome(id uint64, typ, links, reason string) history.Entry {
	return testEntry(id, typ, `{"conversation_id":"a","occurred_at":"2020-01-01T00:00:00Z","reason":"`+reason+`"`+links+`}`)
}
func testSendItem(t *testing.T, f *Fold, id, order, rev uint64, status string, active, shown bool) Item {
	t.Helper()
	for _, item := range f.Items() {
		if item.ID == id {
			if item.Order != order || item.Rev != rev || item.Kind != "user_message" || item.Status != status || item.Active != active || item.Shown != shown || item.Turn != "" || item.Parent != 0 || item.Subtype != "" {
				t.Fatalf("unexpected send item: %#v", item)
			}
			return item
		}
	}
	t.Fatalf("missing send %d: %#v", id, f.Items())
	return Item{}
}

func TestSendDelivery(t *testing.T) {
	t.Parallel()
	for _, source := range []*history.SessionProvenance{nil, testSource("claude", "receiver"), testSource("codex", "receiver"), testSource("none", "")} {
		t.Run(sourceName(source), func(t *testing.T) {
			accept := testAcceptance(1, "phone")
			accept.Session = testSource("claude", "enqueue")
			message := testEntry(3, "message", `{"role":"user","text":"safe\ndelivered","attachment_ids":["delivered-file"],"device_name":"phone","client_version":"v2","message_id":"different","client_sent_at":"different time","queued_msg_id":90,"sent_now":true,"future":"saved"}`)
			message.Session = source
			outcome := testSendOutcome(4, "send_delivered", `,"accepted_entry_id":1,"delivery_entry_id":3`, "delivered")
			outcome.Session = testSource("codex", "wrong")
			hidden := false
			outcome.Shown = &hidden
			entries := []history.Entry{accept, testMessage(2), message, outcome}
			f := New("a")
			testFeed(t, f, accept)
			queued := testSendItem(t, f, 1, 0, 1, "queued", true, true)
			if queued.Session != "enqueue" || queued.Agent != "claude" || queued.Summary != "queued text" || !reflect.DeepEqual(queued.Content, accept.Payload) {
				t.Fatal(queued)
			}
			f = testMainReplay(t, entries)
			if len(f.Items()) != 2 {
				t.Fatalf("linked row not reconciled: %#v", f.Items())
			}
			item := testSendItem(t, f, 1, 3, 4, "delivered", false, true)
			want := history.SessionProvenance{}
			if source != nil {
				want = *source
			}
			if item.Session != want.SessionID || item.Agent != recordedAgent(want.Kind) || item.NoChild != (want.Kind == "none") {
				t.Fatal(item)
			}
			if item.Summary != "safe delivered" {
				t.Fatal(item)
			}
			for key, want := range map[string]any{"text": "safe\ndelivered", "attachment_ids": []any{"delivered-file"}, "device_id": "phone", "message_id": "same", "accepted_at": "2026-01-01T00:00:00Z", "client_sent_at": "client time", "device_name": "phone", "client_version": "v2", "queued_msg_id": float64(90), "sent_now": true, "future": "saved"} {
				testContent(t, item, key, want)
			}
			var saved map[string]any
			_ = json.Unmarshal(outcome.Payload, &saved)
			testContent(t, item, "outcome", saved)
			testSendItem(t, f, 2, 2, 2, "delivered", false, true)
		})
	}
}

func TestSendTerminalStates(t *testing.T) {
	t.Parallel()
	for _, source := range []*history.SessionProvenance{nil, testSource("claude", "s"), testSource("codex", "s"), testSource("none", "")} {
		for _, tc := range []struct {
			typ, reason, status string
			shown               bool
		}{
			{"send_dropped", "removed", "dropped", false}, {"send_dropped", "give_up", "dropped", false}, {"send_lost", "daemon_restart", "lost", true},
		} {
			t.Run(sourceName(source)+"/"+tc.reason, func(t *testing.T) {
				accept := testAcceptance(1, "phone")
				accept.Session = source
				outcome := testSendOutcome(2, tc.typ, `,"accepted_entry_id":1`, tc.reason)
				outcome.Session = testSource("codex", "replacement")
				opposite := !tc.shown
				outcome.Shown = &opposite
				entries := []history.Entry{accept, outcome, testMessage(3), testSendOutcome(4, "send_delivered", `,"accepted_entry_id":1,"delivery_entry_id":3`, "delivered"), testAcceptance(5, "desktop")}
				f := testMainReplay(t, entries)
				if len(f.Items()) != 3 {
					t.Fatal(f.Items())
				}
				item := testSendItem(t, f, 1, 0, 2, tc.status, false, tc.shown)
				if item.NoChild != (source != nil && source.Kind == "none") || (source == nil && (item.Session != "" || item.Agent != "")) || (source != nil && (item.Session != source.SessionID || item.Agent != recordedAgent(source.Kind))) {
					t.Fatal(item)
				}
				testContent(t, item, "text", "queued\ntext")
				var saved map[string]any
				_ = json.Unmarshal(outcome.Payload, &saved)
				testContent(t, item, "outcome", saved)
				testSendItem(t, f, 3, 3, 3, "delivered", false, true)
				testSendItem(t, f, 5, 0, 5, "queued", true, true)
			})
		}
	}
}

func TestSendLinksAndTerminals(t *testing.T) {
	t.Parallel()
	for _, source := range []*history.SessionProvenance{nil, testSource("claude", "s"), testSource("codex", "s")} {
		t.Run(sourceName(source), func(t *testing.T) {
			entries := []history.Entry{
				testAcceptance(1, "phone"), testAcceptance(2, "desktop"), testMessage(3),
				testSendOutcome(4, "send_delivered", `,"accepted_entry_id":1,"delivery_entry_id":3`, "delivered"),
				testSendOutcome(5, "send_delivered", `,"accepted_entry_id":2,"delivery_entry_id":3`, "delivered"),
				testMessage(6),
				testSendOutcome(7, "send_delivered", `,"accepted_entry_id":1,"delivery_entry_id":6`, "delivered"),
				testSendOutcome(8, "send_dropped", `,"accepted_entry_id":1`, "removed"),
				testSendOutcome(9, "send_lost", `,"accepted_entry_id":1`, "daemon_restart"),
				testSendOutcome(10, "send_delivered", `,"accepted_entry_id":2,"delivery_entry_id":6`, "delivered"),
			}
			for i := range entries {
				entries[i].Session = source
			}
			f := testMainReplay(t, entries)
			first := New("a")
			testFeed(t, first, entries[:4]...)
			if len(f.Items()) != 2 || !reflect.DeepEqual(first.Items()[0], f.Items()[0]) {
				t.Fatal("conflicting terminal changed first delivery", f.Items())
			}
			testContent(t, testSendItem(t, f, 1, 3, 4, "delivered", false, true), "device_id", "phone")
			testContent(t, testSendItem(t, f, 2, 6, 10, "delivered", false, true), "device_id", "desktop")
		})
	}
	for _, links := range []string{
		``, `,"accepted_entry_id":0,"delivery_entry_id":3`, `,"accepted_entry_id":99,"delivery_entry_id":3`,
		`,"accepted_entry_id":9007199254740992,"delivery_entry_id":3`, `,"accepted_entry_id":3,"delivery_entry_id":1`,
		`,"accepted_entry_id":1`, `,"accepted_entry_id":1,"delivery_entry_id":0`, `,"accepted_entry_id":1,"delivery_entry_id":99`,
		`,"accepted_entry_id":1,"delivery_entry_id":9007199254740992`, `,"accepted_entry_id":1,"delivery_entry_id":2`,
		`,"accepted_entry_id":1,"delivery_entry_id":1`, `,"accepted_entry_id":1,"delivery_entry_id":4`, `,"accepted_entry_id":5,"delivery_entry_id":3`,
		`,"accepted_entry_id":7,"delivery_entry_id":3`, `,"accepted_entry_id":1,"delivery_entry_id":6`,
		`,"accepted_entry_id":1,"delivery_entry_id":8`, `,"accepted_entry_id":9,"delivery_entry_id":3`,
		`,"accepted_entry_id":1,"delivery_entry_id":9`,
	} {
		t.Run(links, func(t *testing.T) {
			entries := []history.Entry{testAcceptance(1, "phone"), testEntry(2, "banner", `{"text":"other kind"}`), testMessage(3), testEntry(4, "message", `{"conversation_id":"other","role":"user","text":"foreign"}`), testEntry(5, "send_accepted", `{"conversation_id":"other","text":"foreign"}`), testEntry(6, "message", `{"role":"user","text":null}`), testEntry(7, "send_accepted", `{"text":null}`), testEntry(8, "message", `{"role":"assistant","text":"wrong role"}`), testSendOutcome(9, "send_delivered", links, "delivered"), testSendOutcome(10, "send_delivered", `,"accepted_entry_id":1,"delivery_entry_id":3`, "delivered")}
			f := testMainReplay(t, entries)
			if len(f.Items()) != 2 {
				t.Fatal(f.Items())
			}
			testSendItem(t, f, 1, 3, 10, "delivered", false, true)
		})
	}
}

func TestSendMalformedNeutrality(t *testing.T) {
	t.Parallel()
	cases := []struct{ typ, raw string }{
		{"send_accepted", `{"reason":"unsupported"}`}, {"send_accepted", `{"DEVICE_ID":null}`}, {"send_delivered", `{"ACCEPTED_ENTRY_ID":null,"delivery_entry_id":2,"reason":"delivered"}`}, {"send_accepted", `null`}, {"send_accepted", `[]`}, {"send_accepted", `{`},
		{"send_delivered", `{"accepted_entry_id":1,"delivery_entry_id":2,"reason":"removed"}`},
		{"send_dropped", `{"accepted_entry_id":1,"reason":"delivered"}`},
		{"send_lost", `{"accepted_entry_id":1,"reason":"removed"}`},
		{"send_delivered", `{"accepted_entry_id":1,"delivery_entry_id":2}`},
		{"send_accepted", `{"conversation_id":"other","text":"foreign"}`},
		{"send_delivered", `{"conversation_id":"other","accepted_entry_id":1,"delivery_entry_id":2,"reason":"delivered"}`},
		{"send_delivered", `{"accepted_entry_id":3,"delivery_entry_id":2,"reason":"delivered"}`},
	}
	for typ, reason := range map[string]string{"send_dropped": "removed", "send_lost": "daemon_restart"} {
		for _, links := range []string{"", `,"accepted_entry_id":0`, `,"accepted_entry_id":3`, `,"accepted_entry_id":99`, `,"accepted_entry_id":9007199254740992`} {
			cases = append(cases, struct{ typ, raw string }{typ, `{"reason":"` + reason + `"` + links + `}`})
		}
	}
	for _, field := range []string{"conversation_id", "device_id", "message_id", "text", "accepted_at", "client_sent_at", "accepted_entry_id", "delivery_entry_id", "occurred_at", "reason"} {
		for _, value := range []string{"null", "false", "{}"} {
			for _, typ := range []string{"send_accepted", "send_delivered", "send_dropped", "send_lost"} {
				reason := map[string]string{"send_accepted": "", "send_delivered": "delivered", "send_dropped": "removed", "send_lost": "daemon_restart"}[typ]
				raw := fmt.Sprintf(`{"accepted_entry_id":1,"delivery_entry_id":2,"reason":%q,%q:%s}`, reason, field, value)
				cases = append(cases, struct{ typ, raw string }{typ, raw})
			}
		}
	}
	for _, value := range []string{`[null]`, `[false]`, `4`, `"wrong"`} {
		cases = append(cases, struct{ typ, raw string }{"send_accepted", `{"attachment_ids":` + value + `}`})
	}
	for i, tc := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			prefix := []history.Entry{testAcceptance(1, "phone"), testMessage(2), testMain(3, "assistant_delta", `,"text":"open"`)}
			f, baseline := New("a"), New("a")
			testFeed(t, f, prefix...)
			testFeed(t, baseline, prefix...)
			bad := testEntry(4, tc.typ, tc.raw)
			bad.Session = testSource("codex", "wrong")
			testFeed(t, f, bad)
			baseline.version = 4
			if !reflect.DeepEqual(f, baseline) {
				t.Fatalf("malformed fact changed state: %s %s", tc.typ, tc.raw)
			}
			entries := append(prefix, bad, testSendOutcome(5, "send_delivered", `,"accepted_entry_id":1,"delivery_entry_id":2`, "delivered"))
			f = testMainReplay(t, entries)
			testSendItem(t, f, 1, 2, 5, "delivered", false, true)
		})
	}
}

func TestSendTextRuns(t *testing.T) {
	t.Parallel()
	entries := []history.Entry{testMain(1, "assistant_delta", `,"text":"before"`), testAcceptance(2, "phone"), testMain(3, "assistant_delta", `,"text":" after"`), testMessage(4), testMain(5, "assistant_delta", `,"text":"next"`), testSendOutcome(6, "send_delivered", `,"accepted_entry_id":2,"delivery_entry_id":4`, "delivered"), testMain(7, "assistant_delta", `,"text":" continued"`), testMain(8, "tool_use", `,"tool_use_id":"call","name":"Read"`), testMain(9, "tool_result", `,"tool_use_id":"call","is_error":false`)}
	f := testMainReplay(t, entries)
	if len(f.Items()) != 4 {
		t.Fatal(f.Items())
	}
	testContent(t, testMainItem(t, f, 1, 4, "assistant_message", "done", false), "text", "before after")
	testContent(t, testMainItem(t, f, 5, 8, "assistant_message", "done", false), "text", "next continued")
	testMainItem(t, f, 8, 9, "tool_call", "done", false)
}

func TestSendOwnershipAndProvenance(t *testing.T) {
	t.Parallel()
	boundary := testEntry(1, "session_divider", `{"cause":"agent_switch","previous_session_id":"old","new_session_id":"next","next_agent":"codex","occurred_at":"2026-01-01T00:00:00Z"}`)
	for _, source := range []*history.SessionProvenance{nil, testSource("none", ""), testSource("claude", "receiver")} {
		t.Run(sourceName(source), func(t *testing.T) {
			accept := testAcceptance(2, "phone")
			accept.Session = source
			hidden := false
			accept.Shown = &hidden
			message := testMessage(3)
			message.Session = source
			switched := testEntry(4, "session_divider", `{"cause":"agent_switch","previous_session_id":"next","new_session_id":"later","next_agent":"claude","occurred_at":"2026-01-02T00:00:00Z"}`)
			entries := []history.Entry{boundary, accept, message, switched, testSendOutcome(5, "send_delivered", `,"accepted_entry_id":2,"delivery_entry_id":3`, "delivered")}
			f := testMainReplay(t, entries)
			item := testSendItem(t, f, 2, 3, 5, "delivered", false, false)
			want := history.SessionProvenance{Kind: "codex", SessionID: "next"}
			if source != nil {
				want = *source
			}
			if item.Session != want.SessionID || item.Agent != recordedAgent(want.Kind) || item.NoChild != (want.Kind == "none") {
				t.Fatal(item)
			}
			snapshot := f.Items()
			snapshot[1].Content[0] = '!'
			entries[1].Payload[0] = '!'
			entries[2].Payload[0] = '!'
			entries[4].Payload[0] = '!'
			if !json.Valid(f.Items()[1].Content) {
				t.Fatal("snapshot/input aliases send content")
			}
			other := New("other")
			otherAcceptance := testEntry(2, "send_accepted", `{"conversation_id":"other","text":"other"}`)
			testFeed(t, other, otherAcceptance, testMessage(3), testEntry(4, "send_lost", `{"conversation_id":"other","accepted_entry_id":2,"reason":"daemon_restart"}`))
			testSendItem(t, other, 2, 0, 4, "lost", false, true)
			testSendItem(t, f, 2, 3, 5, "delivered", false, false)
		})
	}
}

func TestSendMinimalAndCasing(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{`{}`, `{"attachment_ids":["only-file"]}`, `{"attachment_ids":null}`, `{"DEVICE_ID":"phone","MESSAGE_ID":"first","Message_Id":"last","CLIENT_SENT_AT":"accepted time"}`} {
		t.Run(raw, func(t *testing.T) {
			entries := []history.Entry{testEntry(1, "send_accepted", raw), testEntry(2, "message", `{"role":"user","text":"","message_id":"delivered id","client_sent_at":"delivered time"}`), testSendOutcome(3, "send_delivered", `,"accepted_entry_id":1,"delivery_entry_id":2`, "delivered")}
			f := testMainReplay(t, entries)
			item := testSendItem(t, f, 1, 2, 3, "delivered", false, true)
			if len(f.Items()) != 1 || item.Summary != "user message" {
				t.Fatal(f.Items())
			}
			id, sent := "delivered id", "delivered time"
			if strings.Contains(raw, `"DEVICE_ID"`) {
				id, sent = "last", "accepted time"
				testContent(t, item, "device_id", "phone")
			}
			testContent(t, item, "message_id", id)
			testContent(t, item, "client_sent_at", sent)
			testContent(t, item, "attachment_ids", nil)
		})
	}
}
