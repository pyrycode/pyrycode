package thread

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
)

func TestReferenceWorkRendering(t *testing.T) {
	work := []history.Entry{
		testEntry(1, "assistant_delta", "{ \"turn_id\":\"t\", \"text\":\" <&>\\u2003a\\u0001 \", \"future\": [1, 2] }"),
		testMain(2, "assistant_delta", `,"text":""`),
		testMain(3, "assistant_delta", ",\"text\":\"\xe2\""),
		testMain(4, "assistant_delta", ",\"text\":\"\x82\xac\""),
		testMain(5, "tool_result", `,"tool_use_id":"r","is_error":false,"detail":"early <&>"`),
		testMain(6, "tool_use", `,"tool_use_id":"r","name":"Read","future":[1, 2]`),
		testMain(7, "tool_denied", `,"tool_use_id":"r"`),
		testMain(8, "tool_use", `,"tool_use_id":"d","name":"Write"`),
		testMain(9, "tool_denied", `,"tool_use_id":"d","reason":"denied"`),
		testMain(10, "tool_use", `,"tool_use_id":"i","name":"Read"`),
		testMain(11, "main_tool_interrupted", `,"tool_call_id":"i","cause":"stop","occurred_at":"2026-01-01T00:00:00Z"`),
		testMain(12, "tool_use", `,"tool_use_id":"e","name":"Read"`),
		testMain(13, "turn_end", `,"stop_reason":"end_turn"`),
		testMain(14, "assistant_delta", `,"text":"late"`),
	}
	child := testFoldCorpus(18)
	late, empty := child[9], child[9]
	late.Payload = []byte(strings.ReplaceAll(string(late.Payload), `"text":"unfinished"`, `"text":" late <&>\u2003"`))
	empty.Payload = []byte(strings.ReplaceAll(string(empty.Payload), `"text":"unfinished"`, `"text":""`))
	child = append(slices.Clone(child[:11]), append([]history.Entry{late, empty}, child[11:]...)...)
	ending := empty
	ending.Type = "turn_end"
	ending.Payload = []byte(strings.ReplaceAll(string(empty.Payload), `"text":""`, `"stop_reason":"end_turn"`))
	child = append(child, ending, late)
	for i := range child {
		child[i].ID = uint64(i + 1)
	}
	for _, entries := range [][]history.Entry{work, child, testFoldCorpus(100)} {
		for _, sourceKind := range []string{"claude", "codex"} {
			for i := range entries {
				entries[i].Session = testSource(sourceKind, "s")
			}
			legacy := New("a")
			f, source := testReferenceFold(entries)
			for i, e := range entries {
				source.chunk = entries[i : i+1]
				want, err := legacy.FeedChanges([]history.Entry{e})
				if err != nil {
					t.Fatal(err)
				}
				got, err := f.FeedChanges([]history.Entry{e})
				if err != nil || !reflect.DeepEqual(got, want) || !reflect.DeepEqual(f.touches, legacy.touches) {
					t.Fatalf("%s entry %d observation/touches differ: %v\ngot %#v\nwant %#v", sourceKind, e.ID, err, got, want)
				}
				if len(entries) == len(child) && sourceKind == "claude" && (e.ID == 11 || e.ID == 12 || e.ID == 13 || e.ID == 14) {
					var text Item
					for _, item := range f.Items() {
						if item.ID == 10 {
							text = item
						}
					}
					if text.ID == 0 || text.Active != (e.ID == 14) {
						t.Fatal("child closure/reopening scenario was not exercised", e.ID, text)
					}
					if e.ID >= 12 && !strings.Contains(messageText(text), "late <&>") {
						t.Fatal("inactive child lost late append")
					}
				}
				source.remember(source.chunk)
				source.chunk = nil
				for index, item := range f.items {
					if item.Kind == "assistant_message" || (item.Kind == "tool_call" && sourceKind == "codex") {
						if len(item.Content) != 0 || (item.Kind == "assistant_message" && item.Summary != "") {
							t.Fatalf("entry %d retained converted content/summary at %d", e.ID, index)
						}
						if child := f.children[index]; child != nil && child.childSnapshot != nil && (len(child.base.Content) != 0 || len(child.previous.Content) != 0 || (item.Kind == "assistant_message" && (child.base.Summary != "" || child.previous.Summary != ""))) {
							t.Fatal("child copies retained converted bytes")
						}
					}
					if item.Kind == "tool_call" && sourceKind == "claude" {
						var creating struct {
							Name string `json:"name"`
						}
						_ = json.Unmarshal(entries[item.ID-1].Payload, &creating)
						if creating.Name != "Bash" && creating.Name != "local_bash" && len(item.Content) != 0 {
							t.Fatal("ordinary Claude tool retained content")
						}
					}
				}
			}
			// Resolve old text and terminal entries after FIFO eviction.
			for id := uint64(len(entries) + 1); id < uint64(len(entries)+300); id++ {
				source.remember([]history.Entry{testEntry(id, "ignored", `{}`)})
			}
			got, err := f.Render()
			if err != nil || !reflect.DeepEqual(got, legacy.Items()) {
				t.Fatal("evicted references changed output", err)
			}
			if len(got) > 0 {
				got[0].Content[0] = 'X'
				if !reflect.DeepEqual(f.Items(), legacy.Items()) {
					t.Fatal("snapshot was not detached")
				}
			}
		}
	}
}

func TestReferenceWorkSourceFailure(t *testing.T) {
	entries := []history.Entry{
		testMain(1, "assistant_delta", `,"text":"a"`),
		testMain(2, "assistant_delta", `,"text":"b"`),
		testMain(3, "tool_use", `,"tool_use_id":"r","name":"Read"`),
		testMain(4, "tool_result", `,"tool_use_id":"r","is_error":false`),
	}
	for _, missing := range []uint64{1, 2, 3, 4} {
		f, source := testReferenceFold(entries)
		testFeed(t, f, entries...)
		lookup := source.lookup
		source.lookup = func(ids []uint64) ([]history.Entry, error) {
			out, err := lookup(ids)
			var filtered []history.Entry
			for _, e := range out {
				if e.ID != missing {
					filtered = append(filtered, e)
				}
			}
			return filtered, err
		}
		if items, err := f.Render(); err == nil || items != nil {
			t.Fatalf("missing %d allowed publication", missing)
		}
		if o := f.Observe(); o.State != StateUnavailable || o.Version != 0 || o.Items != nil || o.Err != ErrUnavailable {
			t.Fatal("failed observation usable", o)
		}
		source.lookup = lookup
		if _, err := f.Render(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReferenceToolClassification(t *testing.T) {
	for _, sourceKind := range []string{"claude", "codex"} {
		for _, name := range []string{"Read", "Bash", "local_bash", "Agent", "Task"} {
			e := testMain(1, "tool_use", `,"tool_use_id":"r","name":`+string(testJSON(t, name)))
			e.Session = testSource(sourceKind, "s")
			f, _ := testReferenceFold([]history.Entry{e})
			testFeed(t, f, e)
			item := f.items[0]
			if len(item.Content) != 0 {
				t.Fatal("wrong lifecycle selection", sourceKind, name)
			}
		}
	}
}
