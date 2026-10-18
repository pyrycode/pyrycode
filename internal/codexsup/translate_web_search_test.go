package codexsup

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func TestTranslateWebSearch(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, query, action, want string
	}{
		{"empty start and completion", "", `null`, ""},
		{"plural preferred", "top", `{"type":"search","queries":["first","","second"],"query":"singular"}`, "first\nsecond"},
		{"singular fallback", "top", `{"type":"search","queries":[""],"query":"singular"}`, "singular"},
		{"top fallback", "top", `{"type":"search","queries":[],"query":""}`, "top"},
		{"search fields absent", "top", `{"type":"search"}`, "top"},
		{"search fields null", "top", `{"type":"search","queries":null,"query":null}`, "top"},
		{"queries absent", "top", `{"type":"search","query":"singular"}`, "singular"},
		{"query null", "top", `{"type":"search","queries":["first"],"query":null}`, "first"},
		{"queries null", "top", `{"type":"search","queries":null,"query":"singular"}`, "singular"},
		{"open page", "top", `{"type":"openPage","url":"https://example.invalid/page"}`, "https://example.invalid/page"},
		{"open absent", "top", `{"type":"openPage"}`, ""},
		{"open null", "top", `{"type":"openPage","url":null}`, ""},
		{"open empty", "top", `{"type":"openPage","url":""}`, ""},
		{"find", "top", `{"type":"findInPage","url":"https://example.invalid/page","pattern":"needle"}`, "https://example.invalid/page\nneedle"},
		{"find fields absent", "top", `{"type":"findInPage"}`, ""},
		{"find fields null", "top", `{"type":"findInPage","url":null,"pattern":null}`, ""},
		{"find url only", "top", `{"type":"findInPage","url":"https://example.invalid/page"}`, "https://example.invalid/page"},
		{"find pattern only", "top", `{"type":"findInPage","pattern":"needle"}`, "needle"},
		{"find null url", "top", `{"type":"findInPage","url":null,"pattern":"needle"}`, "needle"},
		{"find null pattern", "top", `{"type":"findInPage","url":"https://example.invalid/page","pattern":null}`, "https://example.invalid/page"},
		{"other", "top", `{"type":"other"}`, "top"},
		{"action absent", "top", "", "top"},
		{"action null", "top", `null`, "top"},
		{"inert text", "$(touch /tmp/never-execute)", `null`, "$(touch /tmp/never-execute)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := map[string]any{"type": "webSearch", "id": "ws_1", "query": tc.query, "results": nil}
			if tc.action != "" {
				item["action"] = json.RawMessage(tc.action)
			}
			// Each optional-field case must also be a valid schema item.
			s := loadSchema(t)
			def, err := s.resolve("#/definitions/v2/ThreadItem")
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(item)
			if err != nil {
				t.Fatal(err)
			}
			var value any
			if err := json.Unmarshal(raw, &value); err != nil {
				t.Fatal(err)
			}
			if err := s.validate(def, value); err != nil {
				t.Fatal(err)
			}
			// Opaque results never contribute text, even if they hold display-like fields.
			item["results"] = []any{map[string]any{"query": "RESULTS MUST NOT APPEAR", "url": "file:///etc/passwd"}}
			raw, err = json.Marshal(map[string]any{"item": item})
			if err != nil {
				t.Fatal(err)
			}
			tr := NewTranslator("")
			wantStart := []turnevent.Event{turnevent.ToolStart{ToolCallID: "ws_1", Title: "web_search", Kind: turnevent.ToolKindFetch}}
			if got := tr.Translate("item/started", raw); !reflect.DeepEqual(got, wantStart) {
				t.Fatalf("start = %#v, want %#v", got, wantStart)
			}
			want := turnevent.ToolUpdate{ToolCallID: "ws_1", Status: turnevent.ToolStatusCompleted}
			if tc.want != "" {
				want.Content = turnevent.TextContent{Text: tc.want}
			}
			if got := tr.Translate("item/completed", raw); !reflect.DeepEqual(got, []turnevent.Event{want}) {
				t.Fatalf("completion = %#v, want %#v", got, want)
			}
		})
	}
}

func TestTranslateWebSearchIdentityReuse(t *testing.T) {
	tr := NewTranslator("")
	frames := readFrames(t, filepath.Join("testdata", "handbuilt", "web_search.jsonl"))
	for range 2 {
		var events []turnevent.Event
		for _, f := range frames {
			events = append(events, tr.Translate(f.Method, f.Params)...)
		}
		want := []turnevent.Event{
			turnevent.ToolStart{ToolCallID: "ws_1", Title: "web_search", Kind: turnevent.ToolKindFetch},
			turnevent.ToolUpdate{ToolCallID: "ws_1", Status: turnevent.ToolStatusCompleted, Content: turnevent.TextContent{Text: "first\nsecond"}},
		}
		if !reflect.DeepEqual(events, want) {
			t.Fatalf("events = %#v, want %#v", events, want)
		}
	}
}

func TestTranslateWebSearchBounds(t *testing.T) {
	const limit = 16 << 10
	for _, action := range []map[string]any{
		{"type": "search", "queries": []string{strings.Repeat("界", 3000), strings.Repeat("海", 3000)}},
		{"type": "findInPage", "url": strings.Repeat("界", 3000), "pattern": strings.Repeat("海", 3000)},
		{"type": "openPage", "url": strings.Repeat("界", 6000)},
		nil,
	} {
		raw, err := json.Marshal(map[string]any{"item": map[string]any{
			"type": "webSearch", "id": "large", "query": strings.Repeat("界", 6000), "action": action,
		}})
		if err != nil {
			t.Fatal(err)
		}
		events := NewTranslator("").Translate("item/completed", raw)
		updates := only[turnevent.ToolUpdate](events)
		if len(events) != 1 || len(updates) != 1 {
			t.Fatalf("completion = %#v", events)
		}
		content, ok := updates[0].Content.(turnevent.TextContent)
		if !ok || len(content.Text) > limit || len(content.Text) < limit-3 || !utf8.ValidString(content.Text) {
			t.Fatalf("bounded text: type %T, bytes %d, valid %v", updates[0].Content, len(content.Text), utf8.ValidString(content.Text))
		}
		if updates[0].ResultDetail != "" {
			t.Fatal("peer text reached ResultDetail")
		}
	}
}

func TestTranslateWebSearchUndecodable(t *testing.T) {
	for _, method := range []string{"item/started", "item/completed"} {
		got := NewTranslator("").Translate(method, json.RawMessage(`{"item":{"type":"webSearch","id":"ws_1","action":[]}}`))
		if len(got) != 1 {
			t.Fatalf("%s: events = %#v", method, got)
		}
		u, ok := got[0].(turnevent.Unrecognized)
		if !ok || u.Site != turnevent.UnrecognizedUndecodable {
			t.Fatalf("%s: event = %#v", method, got[0])
		}
	}
}
