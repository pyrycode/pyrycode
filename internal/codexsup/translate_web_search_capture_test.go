package codexsup

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// TestTranslateCapturedWebSearch pins the observed field timing and tool rows.
// Only the unchanged item pair is publishable; version provenance lives in the PR.
func TestTranslateCapturedWebSearch(t *testing.T) {
	t.Parallel()
	const path = "testdata/capture/web_search.jsonl"
	const id = "exec-cb2e3207-3fb8-4f3c-b836-677222dc5ed3"
	const query = "official Go programming language release notes"
	frames := readFrames(t, path)
	if len(frames) != 2 {
		t.Fatal("capture must contain only the web-search pair; bootstrap/account metadata stays private")
	}
	var pair []frame
	for _, f := range frames {
		if f.Kind != "notification" {
			continue
		}
		var p struct {
			Item struct {
				Type string `json:"type"`
			} `json:"item"`
		}
		if err := json.Unmarshal(f.Params, &p); err != nil {
			t.Fatal(err)
		}
		if p.Item.Type == "webSearch" {
			pair = append(pair, f)
		}
	}
	if len(pair) != 2 || pair[0].Method != "item/started" || pair[1].Method != "item/completed" {
		t.Fatal("capture must contain exactly one ordered webSearch started/completed pair")
	}
	s := loadSchema(t)
	def, err := s.resolve("#/definitions/v2/ThreadItem")
	if err != nil {
		t.Fatal(err)
	}
	for i, f := range pair {
		var p struct {
			Item map[string]json.RawMessage `json:"item"`
		}
		if err := json.Unmarshal(f.Params, &p); err != nil {
			t.Fatal(err)
		}
		var value any
		if err := json.Unmarshal(f.Params, &value); err != nil {
			t.Fatal(err)
		}
		if err := s.validate(def, value.(map[string]any)["item"]); err != nil {
			t.Fatalf("%s item does not match the pinned schema: %v", f.Method, err)
		}
		if string(p.Item["id"]) != `"`+id+`"` {
			t.Fatalf("%s: unexpected webSearch identity", f.Method)
		}
		if i == 0 {
			if string(p.Item["query"]) != `""` || string(p.Item["action"]) != "null" || string(p.Item["results"]) != "null" {
				t.Fatal("start must explicitly carry empty query and null action/results")
			}
			continue
		}
		var action map[string]json.RawMessage
		if err := json.Unmarshal(p.Item["action"], &action); err != nil {
			t.Fatal(err)
		}
		wantQuery, _ := json.Marshal(query)
		if string(p.Item["query"]) != string(wantQuery) || string(action["type"]) != `"search"` ||
			string(action["query"]) != string(wantQuery) || string(action["queries"]) != "null" {
			t.Fatal("completion must carry the observed singular search query and explicitly null plural queries")
		}
		var results []json.RawMessage
		if err := json.Unmarshal(p.Item["results"], &results); err != nil || len(results) == 0 {
			t.Fatal("completion must contain populated opaque results")
		}
	}
	events := replay(t, path, captureModel)
	wantStart := []turnevent.ToolStart{{ToolCallID: id, Title: "web_search", Kind: turnevent.ToolKindFetch}}
	if got := only[turnevent.ToolStart](events); !reflect.DeepEqual(got, wantStart) {
		t.Fatalf("starts = %#v, want %#v", got, wantStart)
	}
	wantUpdate := []turnevent.ToolUpdate{{ToolCallID: id, Status: turnevent.ToolStatusCompleted, Content: turnevent.TextContent{Text: query}}}
	if got := only[turnevent.ToolUpdate](events); !reflect.DeepEqual(got, wantUpdate) {
		t.Fatalf("updates = %#v, want query only with no opaque results or result detail", got)
	}
	for _, u := range only[turnevent.Unrecognized](events) {
		if u.Kind == "webSearch" {
			t.Fatal("webSearch produced an unrecognized card")
		}
	}
}
