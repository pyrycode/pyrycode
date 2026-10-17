package thread

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"testing"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
)

func TestReferenceEqualCompanion(t *testing.T) {
	for _, tool := range []string{"Agent", "Bash"} {
		for _, nested := range []bool{false, true} {
			for _, mappedFirst := range []bool{false, true} {
				for _, kind := range []string{"call", "denial", "task", "link"} {
					t.Run(fmt.Sprint(tool, nested, mappedFirst, kind), func(t *testing.T) {
						launch := testMain(3, "tool_use", fmt.Sprintf(`,"tool_use_id":"c","name":%q`, tool))
						if nested {
							launch = testChild(3, "tool_use", "root", fmt.Sprintf(`,"tool_use_id":"c","name":%q`, tool))
						}
						child := testEntry(4, "ignored", `{}`)
						if tool == "Agent" {
							child = testChild(4, "assistant_delta", "c", `,"text":"unfinished"`)
						}
						link := testEntry(5, "ignored", `{}`)
						durable := testDurable(6, "agent_call_result", `,"turn_id":"t","tool_call_id":"c","tool_use_id":"c","is_error":false,"status":"completed","summary":"same <&>"`)
						mappedType, field := "tool_result", "result"
						if kind == "denial" {
							durable = testDurable(6, "agent_call_denied", `,"turn_id":"t","tool_call_id":"c","tool_use_id":"c","status":"denied","summary":"same <&>"`)
							mappedType, field = "tool_denied", "denial"
						} else if kind == "task" {
							link = testMain(5, "background_task_started", `,"task_id":"task","tool_call_id":"c"`)
							durable = testDurable(6, "background_task_outcome", `,"task_id":"task","status":"completed","summary":"same <&>"`)
							mappedType, field = "background_task_updated", "task_report"
						} else if kind == "link" {
							if nested {
								link = testMain(5, "tool_result", `,"tool_use_id":"root","is_error":false`)
							}
							durable = testDurable(6, "background_task_linked", `,"task_id":"task","tool_call_id":"c","summary":"same <&>"`)
							mappedType, field = "background_task_started", "task_link"
						}
						mapped := durable
						mapped.ID, mapped.Type = 7, mappedType
						first, second := durable, mapped
						mappedID := uint64(7)
						if mappedFirst {
							first, second, mappedID = mapped, durable, 6
							first.ID, second.ID = 6, 7
						}
						entries := []history.Entry{
							testDurable(1, "agent_call_observed", `,"tool_call_id":"root","tool":"Agent"`),
							testMain(2, "tool_use", `,"tool_use_id":"root","name":"Agent"`), launch, child, link, first, second,
							testMain(8, "tool_result", `,"tool_use_id":"root","is_error":false`),
							testMain(9, "background_task_started", `,"task_id":"root-task","tool_call_id":"root"`),
						}
						f, source := testReferenceFold(entries)
						f.captureSet = make(map[int]struct{})
						legacy := New("a")
						var capture publicationCapture
						query := newPublicationIndex(nil, nil)
						store := NewStore(nil)
						store.lookup = func(_ context.Context, _ conversations.ConversationID, ids []uint64) ([]history.Entry, error) {
							return source.Lookup(ids)
						}
						var savedQuery *queryIndex
						var savedView, savedItems []Item
						var savedRecipes map[uint64]contentRecipe
						for i, e := range entries {
							source.chunk = entries[i : i+1]
							got, err := f.FeedChanges(source.chunk)
							want, legacyErr := legacy.FeedChanges(source.chunk)
							if err != nil || legacyErr != nil || !reflect.DeepEqual(got, want) || !reflect.DeepEqual(f.touches, legacy.touches) || f.lastShown != legacy.lastShown {
								t.Fatalf("entry %d changed observation/bookkeeping: %v %v\ngot %+v\nwant %+v", e.ID, err, legacyErr, got, want)
							}
							published, changes, err := capture.capture(f)
							if err != nil || !reflect.DeepEqual(changes.Changes, want.Changes) {
								t.Fatalf("entry %d changed publication: %v", e.ID, err)
							}
							query = query.advance(published, capture.affected, f, nil, nil, capture.headers)
							items, err := renderShared(detachPublication(published), capture.recipes, source, f.items)
							if err != nil || !reflect.DeepEqual(items, legacy.Items()) {
								t.Fatalf("entry %d changed published items: %v", e.ID, err)
							}
							if e.ID == 6 {
								savedView, savedItems, savedRecipes = slices.Clone(f.items), legacy.Items(), maps.Clone(f.recipes)
								savedQuery = query
							}
							source.remember(source.chunk)
							source.chunk = nil
							for id := uint64(100); id < 400; id++ {
								source.remember([]history.Entry{testEntry(id, "ignored", `{}`)})
							}
							lookup := source.lookup
							var requested []uint64
							source.lookup = func(ids []uint64) ([]history.Entry, error) {
								requested = append(requested, ids...)
								return lookup(ids)
							}
							items, err = f.Render()
							source.lookup = lookup
							if err != nil || !reflect.DeepEqual(items, legacy.Items()) || (e.ID >= 3 && !slices.Contains(requested, 3)) {
								t.Fatal("evicted live view differs or launch was not evicted", err)
							}
							if e.ID == 7 {
								row, ok := query.lookup(3, false)
								if !ok || row.recipe.fields[field].entry != mappedID || f.recipes[3].fields[field].entry != mappedID || capture.recipes[3].fields[field].entry != mappedID {
									t.Fatal("mapped content provenance was not published")
								}
								if (kind == "task" || (kind == "call" && tool == "Agent")) && items[1].EndedOrder != 6 {
									t.Fatal("companion changed earliest terminal order")
								}
							}
							if savedView != nil {
								// Select a child without its ending ancestor in the page.
								got, err := renderShared(slices.Clone(savedView[1:]), savedRecipes, source, savedView)
								if err != nil || !reflect.DeepEqual(got, savedItems[1:]) {
									t.Fatal("captured child view changed", err)
								}
								wire, _ := json.Marshal(got)
								wantWire, _ := json.Marshal(savedItems[1:])
								if string(wire) != string(wantWire) {
									t.Fatal("captured wire output changed")
								}
								got, err = store.renderItems(context.Background(), testStoreA, savedQuery, slices.Clone(savedView[1:]))
								if err != nil || !reflect.DeepEqual(got, savedItems[1:]) {
									t.Fatal("captured Store query read mutable recipes", err)
								}
							}
							testNoLifecyclePayload(t, f)
						}
					})
				}
			}
		}
	}
}
