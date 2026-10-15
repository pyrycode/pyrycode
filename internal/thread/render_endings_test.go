package thread

import (
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"slices"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
)

func testEndingEntries() []history.Entry {
	return []history.Entry{
		testMain(1, "tool_use", `,"tool_use_id":"root","name":"Agent"`),
		testChild(2, "tool_use", "root", `,"tool_use_id":"nested","name":"Task"`),
		testChild(3, "assistant_delta", "nested", `,"text":"unfinished"`),
		testMain(4, "tool_result", `,"tool_use_id":"root","is_error":false`),
		testChild(5, "assistant_delta", "nested", `,"text":" late"`),
		testMain(6, "background_task_started", `,"task_id":"task","tool_call_id":"root"`),
		testMain(7, "background_task_updated", `,"task_id":"task","status":"completed"`),
	}
}

func TestReferenceInheritedEnding(t *testing.T) {
	entries := testEndingEntries()
	f, source := testReferenceFold(entries)
	f.captureSet = make(map[int]struct{})
	legacy := New("a")
	var capture publicationCapture
	var old []*Item
	var baseline []Item
	var view []Item
	var recipes map[uint64]contentRecipe
	for i, e := range entries {
		source.chunk = entries[i : i+1]
		want, err := legacy.FeedChanges([]history.Entry{e})
		if err != nil {
			t.Fatal(err)
		}
		got, err := f.FeedChanges([]history.Entry{e})
		if err != nil || !reflect.DeepEqual(got, want) || !reflect.DeepEqual(f.touches, legacy.touches) {
			t.Fatalf("entry %d observation mismatch: %v", e.ID, err)
		}
		published, _, err := capture.capture(f)
		if err != nil {
			t.Fatal(err)
		}
		if e.ID == 4 {
			old, baseline = published, detachPublication(published)
			view, recipes = slices.Clone(f.items), maps.Clone(f.recipes)
		}
		if old != nil && !reflect.DeepEqual(detachPublication(old), baseline) {
			t.Fatal("captured view changed")
		}
		if view != nil {
			selected, err := renderShared([]Item{view[2]}, recipes, source, view)
			if err != nil || !reflect.DeepEqual(selected, baseline[2:3]) {
				t.Fatal("captured recipes read mutable fold state", err)
			}
		}
		if e.ID == 4 || e.ID == 5 || e.ID == 7 {
			selected, err := f.render([]Item{f.items[2]})
			if err != nil || !reflect.DeepEqual(selected, legacy.Items()[2:3]) {
				t.Fatal("omitted ancestor changed selected content", err)
			}
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(selected[0].Content, &fields)
			if !reflect.DeepEqual(fields["parent_ending"], legacy.Items()[0].Content) {
				// encoding/json compacts and escapes embedded raw JSON.
				if !reflect.DeepEqual(testJSON(t, fields["parent_ending"]), testJSON(t, legacy.Items()[0].Content)) {
					t.Fatal("original ancestor lost")
				}
			}
			selected[0].Content[0] = 'X'
		}
		if e.ID == 6 && (!f.items[1].Active || !f.items[2].Active) {
			t.Fatal("late link did not reopen nested children")
		}
		for _, r := range f.recipes {
			if field := r.fields["parent_ending"]; len(field.value) != 0 {
				t.Fatal("copied parent ending")
			}
		}
		for _, child := range f.children {
			if child.legacyEnding != nil {
				t.Fatal("legacy child cache used in production")
			}
		}
		source.remember(source.chunk)
		source.chunk = nil
		for id := uint64(100); id < 400; id++ {
			source.remember([]history.Entry{testEntry(id, "ignored", `{}`)})
		}
	}
}

func TestReferenceShellCompanion(t *testing.T) {
	for _, mappedFirst := range []bool{false, true} {
		entries := []history.Entry{
			testDurable(1, "agent_call_result", `,"tool_call_id":"c","status":"completed"`),
			testMain(2, "tool_result", `,"tool_use_id":"c","is_error":false,"result_summary":"mapped"`),
			testMain(3, "main_turn_opened", `,"occurred_at":"2026-01-01T00:00:00Z"`),
			testShellCall(4),
		}
		content := uint64(2)
		if mappedFirst {
			entries[0], entries[1] = entries[1], entries[0]
			entries[0].ID, entries[1].ID = 1, 2
			content = 1
		}
		f, source := testReferenceFold(entries)
		legacy := New("a")
		for i, e := range entries {
			source.chunk = entries[i : i+1]
			testFeed(t, f, e)
			testFeed(t, legacy, e)
			source.remember(source.chunk)
			source.chunk = nil
		}
		if !reflect.DeepEqual(f.Items(), legacy.Items()) {
			t.Fatal("shell companion output differs")
		}
		for _, g := range f.agentGroups {
			for _, c := range g.calls {
				if c.ordinary != nil {
					o := c.ordinary.terminal
					if o == nil || o.id != 1 || o.content != content {
						t.Fatalf("order/content conflated: %#v", o)
					}
					if len(f.legacyOutcomes) != 0 {
						t.Fatal("production saved legacy outcome")
					}
				}
			}
		}
	}
}

func TestReferenceEndingDependencyFailure(t *testing.T) {
	entries := testEndingEntries()[:4]
	f, source := testReferenceFold(entries)
	testFeed(t, f, entries...)
	// Exercise a referenced ancestor's own outcome while agent launch storage
	// remains deferred. Both dependencies belong to this single captured view.
	f.referenceField(0, "result", recipeField{entry: 4})
	want := f.Items()
	for id := uint64(5); id < 300; id++ {
		source.remember([]history.Entry{testEntry(id, "ignored", `{}`)})
	}
	lookup := source.lookup
	for _, failure := range []string{"missing", "unreadable", "malformed", "null"} {
		source.lookup = func(ids []uint64) ([]history.Entry, error) {
			out, err := lookup(ids)
			if failure == "unreadable" {
				return nil, errors.New("private content/path")
			}
			if failure == "missing" {
				return slices.DeleteFunc(out, func(e history.Entry) bool { return e.ID == 4 }), err
			}
			for i := range out {
				if out[i].ID == 4 {
					out[i].Payload = []byte(`{"private"`)
					if failure == "null" {
						out[i].Payload = []byte(`null`)
					}
				}
			}
			return out, err
		}
		if got, err := f.render([]Item{f.items[2]}); got != nil || err != ErrUnavailable {
			t.Fatal("partial inherited candidate escaped", err)
		}
		if o := f.Observe(); o.State != StateUnavailable || o.Err != ErrUnavailable || o.Items != nil || o.Version != 0 {
			t.Fatal("private dependency failure escaped")
		}
	}
	source.lookup = lookup
	if !reflect.DeepEqual(f.Items(), want) {
		t.Fatal("dependency repair failed")
	}
	if got, err := renderShared([]Item{f.items[2]}, f.recipes, source, f.items[1:]); got != nil || err != ErrUnavailable {
		t.Fatal("missing captured ancestor accepted")
	}
}

func TestReferenceEndingOutcomes(t *testing.T) {
	entries := []history.Entry{
		testMain(1, "tool_use", `,"tool_use_id":"root","name":"Agent"`),
		testMain(2, "tool_result", `,"tool_use_id":"early","is_error":false`),
		testChild(3, "tool_use", "root", `,"tool_use_id":"early","name":"Read"`),
		testChild(4, "turn_end", "root", `,"stop_reason":"end_turn"`),
		testChild(5, "tool_use", "root", `,"tool_use_id":"late","name":"Read"`),
	}
	f, source := testReferenceFold(entries)
	legacy := New("a")
	for i, e := range entries {
		source.chunk = entries[i : i+1]
		got, err := f.FeedChanges([]history.Entry{e})
		want, legacyErr := legacy.FeedChanges([]history.Entry{e})
		if err != nil || legacyErr != nil || !reflect.DeepEqual(got, want) || !reflect.DeepEqual(f.touches, legacy.touches) {
			t.Fatal("pending outcome observation differs", err, legacyErr)
		}
		source.remember(source.chunk)
		source.chunk = nil
	}
	if _, ok := reflect.TypeOf(mainOutcome{}).FieldByName("raw"); ok {
		t.Fatal("mainOutcome retains raw payload")
	}
	for id := uint64(6); id < 300; id++ {
		source.remember([]history.Entry{testEntry(id, "ignored", `{}`)})
	}
	testChildItem(t, f, 3, 1, "tool_call", "done", false)
	testChildItem(t, f, 5, 1, "tool_call", "interrupted", false)
	if f.items[1].Rev != 3 || f.items[2].Rev != 5 {
		t.Fatal("pending outcome violated creation revision floor")
	}
	if !reflect.DeepEqual(f.Items(), legacy.Items()) {
		t.Fatal("precreation outcome differs")
	}
}

func TestStoreReferenceEndingRetry(t *testing.T) {
	entries := testEndingEntries()[:4]
	// The inherited ancestor is deferred inline; its ordinary descendant's
	// intrinsic ending dependency must still be resolved after eviction.
	entries = append(slices.Clone(entries[:3]), testChild(4, "turn_end", "nested", `,"stop_reason":"end_turn"`))
	testStoreReferenceRetry(t, entries, 4, "replay")
}
